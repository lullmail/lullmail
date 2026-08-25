/** Minimal WebAuthn JSON bridge. The server emits base64url options; browser
    APIs require ArrayBuffers, then their responses need to become JSON again. */

function fromB64(value: string): ArrayBuffer {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((value.length + 3) % 4);
  const raw = atob(padded);
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
  return bytes.buffer;
}

function toB64(value: ArrayBuffer | null): string | null {
  if (!value) return null;
  const bytes = new Uint8Array(value);
  let raw = "";
  for (const byte of bytes) raw += String.fromCharCode(byte);
  return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

type JSONOptions = Record<string, any>;

export async function createPasskey(payload: JSONOptions): Promise<Record<string, unknown>> {
  const raw = payload.publicKey || payload;
  const publicKey: PublicKeyCredentialCreationOptions = {
    ...raw,
    challenge: fromB64(raw.challenge),
    user: { ...raw.user, id: fromB64(raw.user.id) },
    excludeCredentials: (raw.excludeCredentials || []).map((item: JSONOptions) => ({ ...item, id: fromB64(item.id) })),
  };
  const credential = await navigator.credentials.create({ publicKey }) as PublicKeyCredential | null;
  if (!credential) throw new Error("No passkey was created.");
  const response = credential.response as AuthenticatorAttestationResponse;
  return {
    id: credential.id,
    rawId: toB64(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      attestationObject: toB64(response.attestationObject),
      clientDataJSON: toB64(response.clientDataJSON),
      transports: response.getTransports?.() || [],
    },
  };
}

export async function getPasskey(payload: JSONOptions): Promise<Record<string, unknown>> {
  const raw = payload.publicKey || payload;
  const publicKey: PublicKeyCredentialRequestOptions = {
    ...raw,
    challenge: fromB64(raw.challenge),
    allowCredentials: (raw.allowCredentials || []).map((item: JSONOptions) => ({ ...item, id: fromB64(item.id) })),
  };
  const credential = await navigator.credentials.get({ publicKey }) as PublicKeyCredential | null;
  if (!credential) throw new Error("No passkey was selected.");
  const response = credential.response as AuthenticatorAssertionResponse;
  return {
    id: credential.id,
    rawId: toB64(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      authenticatorData: toB64(response.authenticatorData),
      clientDataJSON: toB64(response.clientDataJSON),
      signature: toB64(response.signature),
      userHandle: toB64(response.userHandle),
    },
  };
}
