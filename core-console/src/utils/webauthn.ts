// Helpers to bridge between the WebAuthn Web API (ArrayBuffer + DOMs) and
// the JSON envelope our backend sends/expects (base64url strings).

function b64urlToBuf(s: string): ArrayBuffer {
  // Pad and convert URL-safe to standard
  const padded = s.replace(/-/g, '+').replace(/_/g, '/').padEnd(s.length + ((4 - s.length % 4) % 4), '=')
  const bin = atob(padded)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

function bufToB64url(buf: ArrayBuffer | Uint8Array): string {
  const arr = buf instanceof ArrayBuffer ? new Uint8Array(buf) : buf
  let bin = ''
  for (let i = 0; i < arr.byteLength; i++) bin += String.fromCharCode(arr[i])
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * Convert backend's PublicKeyCredentialCreationOptions (JSON with base64url
 * fields) into a real options object with ArrayBuffer values.
 */
export function decodeCreationOptions(o: any): CredentialCreationOptions {
  return {
    publicKey: {
      ...o,
      challenge: b64urlToBuf(o.challenge),
      user: {
        ...o.user,
        id: b64urlToBuf(o.user.id)
      },
      excludeCredentials: (o.excludeCredentials ?? []).map((c: any) => ({
        ...c,
        id: b64urlToBuf(c.id)
      }))
    }
  } as CredentialCreationOptions
}

export function decodeRequestOptions(o: any): CredentialRequestOptions {
  return {
    publicKey: {
      ...o,
      challenge: b64urlToBuf(o.challenge),
      allowCredentials: (o.allowCredentials ?? []).map((c: any) => ({
        ...c,
        id: b64urlToBuf(c.id)
      }))
    }
  } as CredentialRequestOptions
}

/**
 * Serialize a navigator.credentials.create() result for POSTing back to
 * the server. Mirrors the structure expected by go-webauthn's
 * ParseCredentialCreationResponseBody.
 */
export function encodeCreationResponse(cred: PublicKeyCredential): any {
  const att = cred.response as AuthenticatorAttestationResponse
  const transports = (att as any).getTransports ? (att as any).getTransports() : []
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      attestationObject: bufToB64url(att.attestationObject),
      clientDataJSON:    bufToB64url(att.clientDataJSON),
      transports
    },
    authenticatorAttachment: cred.authenticatorAttachment,
    clientExtensionResults: cred.getClientExtensionResults()
  }
}

export function encodeAssertionResponse(cred: PublicKeyCredential): any {
  const ar = cred.response as AuthenticatorAssertionResponse
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: bufToB64url(ar.authenticatorData),
      clientDataJSON:    bufToB64url(ar.clientDataJSON),
      signature:         bufToB64url(ar.signature),
      userHandle:        ar.userHandle ? bufToB64url(ar.userHandle) : null
    },
    authenticatorAttachment: cred.authenticatorAttachment,
    clientExtensionResults: cred.getClientExtensionResults()
  }
}

export function isWebAuthnSupported(): boolean {
  return typeof window !== 'undefined'
    && !!window.PublicKeyCredential
    && typeof window.PublicKeyCredential === 'function'
}
