export interface PasskeyStartPayload {
  options_json: string;
  session: string;
}

type JSONRecord = Record<string, unknown>;

export function base64URLToArrayBuffer(value: string): ArrayBuffer {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized.padEnd(normalized.length + ((4 - (normalized.length % 4)) % 4), '=');
  const binary = window.atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let idx = 0; idx < binary.length; idx += 1) {
    bytes[idx] = binary.charCodeAt(idx);
  }
  return bytes.buffer;
}

export function arrayBufferToBase64URL(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = '';
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return window.btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

export function decodePasskeyCreationOptions(optionsJSON: string): CredentialCreationOptions {
  const options = JSON.parse(optionsJSON) as { publicKey: PublicKeyCredentialCreationOptions; mediation?: CredentialMediationRequirement };
  options.publicKey.challenge = base64URLToArrayBuffer(String(options.publicKey.challenge));
  options.publicKey.user.id = base64URLToArrayBuffer(String(options.publicKey.user.id));
  options.publicKey.excludeCredentials = options.publicKey.excludeCredentials?.map((credential) => ({
    ...credential,
    id: base64URLToArrayBuffer(String(credential.id)),
  }));
  return options;
}

export function decodePasskeyRequestOptions(optionsJSON: string): CredentialRequestOptions {
  const options = JSON.parse(optionsJSON) as { publicKey: PublicKeyCredentialRequestOptions; mediation?: CredentialMediationRequirement };
  options.publicKey.challenge = base64URLToArrayBuffer(String(options.publicKey.challenge));
  options.publicKey.allowCredentials = options.publicKey.allowCredentials?.map((credential) => ({
    ...credential,
    id: base64URLToArrayBuffer(String(credential.id)),
  }));
  return options;
}

export function credentialToJSON(credential: PublicKeyCredential): string {
  const response = credential.response;
  const payload: JSONRecord = {
    id: credential.id,
    rawId: arrayBufferToBase64URL(credential.rawId),
    type: credential.type,
    clientExtensionResults: credential.getClientExtensionResults(),
  };

  if ('authenticatorAttachment' in credential && credential.authenticatorAttachment) {
    payload.authenticatorAttachment = credential.authenticatorAttachment;
  }

  if (response instanceof AuthenticatorAttestationResponse) {
    payload.response = {
      attestationObject: arrayBufferToBase64URL(response.attestationObject),
      clientDataJSON: arrayBufferToBase64URL(response.clientDataJSON),
      transports: response.getTransports?.() ?? [],
    };
  } else if (response instanceof AuthenticatorAssertionResponse) {
    payload.response = {
      authenticatorData: arrayBufferToBase64URL(response.authenticatorData),
      clientDataJSON: arrayBufferToBase64URL(response.clientDataJSON),
      signature: arrayBufferToBase64URL(response.signature),
      userHandle: response.userHandle ? arrayBufferToBase64URL(response.userHandle) : undefined,
    };
  } else {
    throw new Error('Unsupported passkey response.');
  }

  return JSON.stringify(payload);
}

export async function createPasskeyCredentialJSON(optionsJSON: string): Promise<string> {
  ensurePasskeySupport();
  const credential = await navigator.credentials.create(decodePasskeyCreationOptions(optionsJSON));
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error('Passkey registration did not return a public key credential.');
  }
  return credentialToJSON(credential);
}

export async function getPasskeyCredentialJSON(optionsJSON: string): Promise<string> {
  ensurePasskeySupport();
  const credential = await navigator.credentials.get(decodePasskeyRequestOptions(optionsJSON));
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error('Passkey login did not return a public key credential.');
  }
  return credentialToJSON(credential);
}

export function ensurePasskeySupport(): void {
  if (!window.PublicKeyCredential || !navigator.credentials) {
    throw new Error('Passkeys are not supported by this browser.');
  }
}
