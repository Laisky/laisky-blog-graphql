package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	gcmd "github.com/Laisky/go-utils/v6/cmd"
	"github.com/Laisky/zap"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	blogModel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	blogOneAPI "github.com/Laisky/laisky-blog-graphql/internal/web/blog/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

const legacyUIDField = "uid"

type ssoMigrationStats struct {
	Scanned        int
	Linked         int
	MissingOneAPI  int
	GeneratedUID   int
	PasswordReset  int
	OIDCIdentities int
	Passkeys       int
	ConfirmedTOTP  int
	Conflicts      int
}

var migrateSSOOneAPICMD = &cobra.Command{
	Use:   "sso-oneapi",
	Short: "preflight or import legacy Mongo SSO identities into OneAPI",
	Args:  gcmd.NoExtraArgs,
	PreRun: func(cmd *cobra.Command, _ []string) {
		if err := initialize(context.Background(), cmd); err != nil {
			log.Logger.Panic("initialize sso migration", zap.Error(err))
		}
	},
	Run: func(cmd *cobra.Command, _ []string) {
		apply, err := cmd.Flags().GetBool("apply")
		if err != nil {
			log.Logger.Panic("read sso migration apply flag", zap.Error(err))
		}
		if err = runMigrateSSOOneAPI(cmd.Context(), apply); err != nil {
			log.Logger.Panic("migrate sso users to oneapi", zap.Error(err))
		}
	},
}

func init() {
	migrateCMD.AddCommand(migrateSSOOneAPICMD)
	migrateSSOOneAPICMD.Flags().Bool("apply", false, "write validated identity mappings and credentials")
}

func loadOneAPIDBOptions() blogOneAPI.Options {
	return blogOneAPI.Options{
		Driver:            gconfig.S.GetString("settings.db.oneapi.driver"),
		DSN:               gconfig.S.GetString("settings.db.oneapi.dsn"),
		SQLitePath:        gconfig.S.GetString("settings.db.oneapi.sqlite_path"),
		SQLiteBusyTimeout: gconfig.S.GetInt("settings.db.oneapi.sqlite_busy_timeout_ms"),
		MaxIdleConns:      gconfig.S.GetInt("settings.db.oneapi.max_idle_conns"),
		MaxOpenConns:      gconfig.S.GetInt("settings.db.oneapi.max_open_conns"),
		ConnMaxLifetime: time.Duration(gconfig.S.GetInt(
			"settings.db.oneapi.conn_max_lifetime_seconds")) * time.Second,
	}
}

func runMigrateSSOOneAPI(ctx context.Context, apply bool) error {
	logger := log.Logger.Named("migrate_sso_oneapi")
	mongoDB, err := blogModel.NewDB(ctx)
	if err != nil {
		return errors.Wrap(err, "open legacy blog mongo database")
	}
	defer func() {
		if closeErr := mongoDB.Close(ctx); closeErr != nil {
			logger.Warn("close legacy blog mongo database", zap.Error(closeErr))
		}
	}()
	oneAPIDB, err := blogOneAPI.NewDB(ctx, loadOneAPIDBOptions())
	if err != nil {
		return errors.Wrap(err, "open oneapi database")
	}
	sqlDB, err := oneAPIDB.DB()
	if err != nil {
		return errors.Wrap(err, "get oneapi sql database")
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			logger.Warn("close oneapi database", zap.Error(closeErr))
		}
	}()
	repo := blogOneAPI.New(logger.Named("repository"), oneAPIDB)
	if err = repo.Prepare(ctx); err != nil {
		return errors.Wrap(err, "prepare oneapi sso repository")
	}

	cursor, err := mongoDB.GetCol("users").Find(ctx, bson.D{})
	if err != nil {
		return errors.Wrap(err, "scan legacy mongo users")
	}
	defer cursor.Close(ctx) //nolint:errcheck
	stats := ssoMigrationStats{}
	for cursor.Next(ctx) {
		var legacy blogModel.User
		if err = cursor.Decode(&legacy); err != nil {
			return errors.Wrap(err, "decode legacy mongo user")
		}
		stats.Scanned++
		if err = migrateOneLegacySSOUser(ctx, repo, mongoDB.GetCol("users"), &legacy, apply, &stats); err != nil {
			stats.Conflicts++
			logger.Warn("legacy sso user requires manual resolution",
				zap.String("legacy_object_id", legacy.ID.Hex()), zap.Error(err))
		}
	}
	if err = cursor.Err(); err != nil {
		return errors.Wrap(err, "iterate legacy mongo users")
	}
	logger.Info("sso oneapi migration summary",
		zap.Bool("apply", apply), zap.Int("scanned", stats.Scanned), zap.Int("linked", stats.Linked),
		zap.Int("missing_oneapi", stats.MissingOneAPI), zap.Int("generated_uid", stats.GeneratedUID),
		zap.Int("password_reset_required", stats.PasswordReset), zap.Int("oidc_identities", stats.OIDCIdentities),
		zap.Int("passkeys", stats.Passkeys), zap.Int("confirmed_totp", stats.ConfirmedTOTP),
		zap.Int("conflicts", stats.Conflicts))
	if stats.Conflicts > 0 || stats.MissingOneAPI > 0 {
		return errors.Errorf("sso migration preflight found %d conflicts and %d mongo-only users", stats.Conflicts, stats.MissingOneAPI)
	}
	return nil
}

func migrateOneLegacySSOUser(ctx context.Context, repo *blogOneAPI.Repo, usersCollection *mongo.Collection,
	legacy *blogModel.User, apply bool, stats *ssoMigrationStats) error {
	account := strings.ToLower(strings.TrimSpace(legacy.Account))
	if account == "" || legacy.ID.IsZero() {
		return errors.New("legacy account or object id is missing")
	}
	identity, err := repo.LookupAccountIdentity(ctx, account)
	if errors.Is(err, blogOneAPI.ErrNotFound) {
		stats.MissingOneAPI++
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "lookup matching oneapi account")
	}
	if _, err = uuid.Parse(identity.UserUUID); err != nil {
		return errors.Wrap(err, "parse matching oneapi user uuid")
	}

	ssoUID := strings.TrimSpace(legacy.UID)
	if ssoUID == "" {
		ssoUID = gutils.UUID7()
		stats.GeneratedUID++
		if apply {
			result, updateErr := usersCollection.UpdateOne(ctx,
				bson.M{"_id": legacy.ID, "$or": []bson.M{{legacyUIDField: bson.M{"$exists": false}}, {legacyUIDField: ""}}},
				bson.M{"$set": bson.M{legacyUIDField: ssoUID}})
			if updateErr != nil {
				return errors.Wrap(updateErr, "persist generated legacy sso uid")
			}
			if result.ModifiedCount != 1 {
				return errors.New("legacy sso uid changed concurrently")
			}
		}
	} else if _, err = uuid.Parse(ssoUID); err != nil {
		return errors.Wrap(err, "parse legacy sso uid")
	}
	if legacy.Password != "" && !identity.HasPassword {
		stats.PasswordReset++
	}
	if err = validateLegacyPasskeys(legacy.Passkeys); err != nil {
		return err
	}
	if !apply {
		return nil
	}

	if err = repo.ImportLegacyUserLink(ctx, identity.UserID, ssoUID, legacy.ID); err != nil {
		return errors.Wrap(err, "import legacy user identity link")
	}
	linkedUser, err := repo.GetByUID(ctx, ssoUID)
	if err != nil {
		return errors.Wrap(err, "reload linked oneapi sso user")
	}
	if err = applyLegacySSOExtensions(ctx, repo, linkedUser, legacy, stats); err != nil {
		return err
	}
	stats.Linked++
	return nil
}

func validateLegacyPasskeys(passkeys []blogModel.PasskeyCredential) error {
	for _, passkey := range passkeys {
		if _, err := decodeLegacyPasskey(passkey); err != nil {
			return errors.Wrap(err, "validate legacy passkey")
		}
	}
	return nil
}

func applyLegacySSOExtensions(ctx context.Context, repo *blogOneAPI.Repo, linkedUser *blogModel.User,
	legacy *blogModel.User, stats *ssoMigrationStats) error {
	for _, oidcIdentity := range legacy.OIDCIdentities {
		if strings.ToLower(strings.TrimSpace(oidcIdentity.Provider)) != "github" {
			return errors.Errorf("unsupported legacy oidc provider %q", oidcIdentity.Provider)
		}
		if _, err := repo.BindOIDCIdentity(ctx, linkedUser.OneAPIID, "github", oidcIdentity.Subject, oidcIdentity.Email); err != nil {
			return errors.Wrap(err, "import legacy github identity")
		}
		stats.OIDCIdentities++
	}
	if legacy.TOTPEnabled && strings.TrimSpace(legacy.TOTPSecret) != "" {
		if err := repo.ImportConfirmedTOTP(ctx, linkedUser.OneAPIID, legacy.TOTPSecret); err != nil {
			return errors.Wrap(err, "import confirmed legacy totp")
		}
		stats.ConfirmedTOTP++
	}
	for _, passkey := range legacy.Passkeys {
		credential, decodeErr := decodeLegacyPasskey(passkey)
		if decodeErr != nil {
			return errors.Wrap(decodeErr, "decode legacy passkey")
		}
		updatedUser, err := repo.ImportPasskey(ctx, linkedUser, passkey.Name, credential)
		if err != nil {
			return errors.Wrap(err, "import legacy passkey")
		}
		linkedUser = updatedUser
		stats.Passkeys++
	}
	return nil
}

func decodeLegacyPasskey(passkey blogModel.PasskeyCredential) (*webauthn.Credential, error) {
	if strings.TrimSpace(passkey.CredentialJSON) != "" {
		var credential webauthn.Credential
		if err := json.Unmarshal([]byte(passkey.CredentialJSON), &credential); err != nil {
			return nil, errors.Wrap(err, "decode legacy passkey json")
		}
		return &credential, nil
	}
	credentialID, err := base64.RawURLEncoding.DecodeString(passkey.ID)
	if err != nil {
		return nil, errors.Wrap(err, "decode legacy passkey id")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(passkey.PublicKey)
	if err != nil {
		return nil, errors.Wrap(err, "decode legacy passkey public key")
	}
	var aaguid []byte
	if passkey.AAGUID != "" {
		aaguid, err = base64.RawURLEncoding.DecodeString(passkey.AAGUID)
		if err != nil {
			return nil, errors.Wrap(err, "decode legacy passkey aaguid")
		}
	}
	transports := make([]protocol.AuthenticatorTransport, 0)
	for _, raw := range strings.Split(passkey.Transport, ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			transports = append(transports, protocol.AuthenticatorTransport(raw))
		}
	}
	return &webauthn.Credential{ID: credentialID, PublicKey: publicKey,
		AttestationType: passkey.AttestationType, Transport: transports,
		Flags:         webauthn.CredentialFlags{BackupEligible: passkey.BackupEligible, BackupState: passkey.BackupState},
		Authenticator: webauthn.Authenticator{AAGUID: aaguid, SignCount: passkey.SignCount}}, nil
}
