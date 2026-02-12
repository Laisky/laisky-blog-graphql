package kms

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewMemoryKMSUsesMaxKEKID verifies the largest KEK ID is used for new encryptions.
func TestNewMemoryKMSUsesMaxKEKID(t *testing.T) {
	client, err := NewMemoryKMS(Settings{KEKs: map[uint16]string{
		2: "this-is-a-long-secret-for-kek-id-2",
		7: "this-is-a-long-secret-for-kek-id-7",
	}})
	require.NoError(t, err)
	require.NotNil(t, client)

	encrypted, err := client.Encrypt(context.Background(), []byte("hello"), []byte("aad"))
	require.NoError(t, err)
	require.NotNil(t, encrypted)
	require.Equal(t, uint16(7), encrypted.KekID)
	require.NotEmpty(t, encrypted.DekID)

	plaintext, err := client.Decrypt(context.Background(), encrypted, []byte("aad"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(plaintext))
}

// TestNewMemoryKMSExposesAllKEKs verifies all configured KEKs are retained in KMS.
func TestNewMemoryKMSExposesAllKEKs(t *testing.T) {
	client, err := NewMemoryKMS(Settings{KEKs: map[uint16]string{
		1: "this-is-a-long-secret-for-kek-id-1",
		9: "this-is-a-long-secret-for-kek-id-9",
	}})
	require.NoError(t, err)

	keks, err := client.Keks(context.Background())
	require.NoError(t, err)
	require.Len(t, keks, 2)
	require.Contains(t, keks, uint16(1))
	require.Contains(t, keks, uint16(9))
}

// TestNewMemoryKMSRejectsInvalidInput verifies invalid KEK input is rejected.
func TestNewMemoryKMSRejectsInvalidInput(t *testing.T) {
	_, err := NewMemoryKMS(Settings{})
	require.Error(t, err)

	_, err = NewMemoryKMS(Settings{KEKs: map[uint16]string{1: "short-key"}})
	require.Error(t, err)
}
