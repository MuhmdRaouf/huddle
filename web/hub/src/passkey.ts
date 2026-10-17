// passkey.ts — the WebAuthn glue the pairing and step-up ceremonies run through: base64url in
// and out of ArrayBuffers, the server's JSON options decoded into the browser's own creation and
// request shapes, and the browser's credential of either ceremony encoded back into the JSON the
// finish routes speak. No library: navigator.credentials is the whole API.

/** One credential as the finish routes read it: every ArrayBuffer base64url. */
export type CredentialJSON = {
  id: string;
  rawId: string;
  type: string;
  response:
    | { attestationObject: string; clientDataJSON: string }
    | {
        authenticatorData: string;
        clientDataJSON: string;
        signature: string;
        userHandle: string | null;
      };
};

/** Reads one JSON value as an object; a non-object reads as empty. */
const asObject = (v: unknown): Record<string, unknown> =>
  v !== null && typeof v === "object" ? (v as Record<string, unknown>) : {};

/** Reads one string field; a missing or non-string one reads as "". */
const text = (o: Record<string, unknown>, key: string): string => {
  const v = o[key];
  return typeof v === "string" ? v : "";
};

/** Encodes bytes as base64url: the standard alphabet's + and / swapped, the padding dropped. */
export function bufToB64url(b: ArrayBuffer): string {
  let bin = "";
  for (const byte of new Uint8Array(b)) bin += String.fromCharCode(byte);
  return btoa(bin).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

/** Reads one base64url string back as bytes, padding or not. */
export function b64urlToBuf(s: string): ArrayBuffer {
  const std = s.replaceAll("-", "+").replaceAll("_", "/");
  const bin = atob(std + "=".repeat((4 - (std.length % 4)) % 4));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

/** Decodes every id of one credential-descriptor list the server wrote. */
const descriptors = (list: unknown): PublicKeyCredentialDescriptor[] | undefined => {
  if (!Array.isArray(list)) return undefined;
  return list.map((d) => {
    const desc = asObject(d);
    const id = text(desc, "id");
    return id ? { ...desc, id: b64urlToBuf(id) } : desc;
  }) as unknown as PublicKeyCredentialDescriptor[];
};

/** Decodes the server's creation object: the challenge, the user id and any excluded
 *  credentials' ids become buffers; every other field passes through as it came. */
export function creationFromJSON(o: unknown): { publicKey: PublicKeyCredentialCreationOptions } {
  const src = asObject(asObject(o).publicKey);
  const user = asObject(src.user);
  const out: Record<string, unknown> = {
    ...src,
    challenge: b64urlToBuf(text(src, "challenge")),
    user: { ...user, id: b64urlToBuf(text(user, "id")) },
  };
  const excluded = descriptors(src.excludeCredentials);
  if (excluded) out.excludeCredentials = excluded;
  return { publicKey: out as unknown as PublicKeyCredentialCreationOptions };
}

/** Decodes the server's assertion object: the challenge and any allowed credentials' ids become
 *  buffers; every other field passes through as it came. */
export function assertionFromJSON(o: unknown): { publicKey: PublicKeyCredentialRequestOptions } {
  const src = asObject(asObject(o).publicKey);
  const out: Record<string, unknown> = { ...src, challenge: b64urlToBuf(text(src, "challenge")) };
  const allowed = descriptors(src.allowCredentials);
  if (allowed) out.allowCredentials = allowed;
  return { publicKey: out as unknown as PublicKeyCredentialRequestOptions };
}

/** Encodes one browser credential for a finish route: the attestation's or the assertion's
 *  buffers as base64url, with a null userHandle kept null. */
export function credentialToJSON(c: PublicKeyCredential): CredentialJSON {
  const head = { id: c.id, rawId: bufToB64url(c.rawId), type: c.type };
  const r = c.response as AuthenticatorAttestationResponse | AuthenticatorAssertionResponse;
  if ("attestationObject" in r) {
    return {
      ...head,
      response: {
        attestationObject: bufToB64url(r.attestationObject),
        clientDataJSON: bufToB64url(r.clientDataJSON),
      },
    };
  }
  return {
    ...head,
    response: {
      authenticatorData: bufToB64url(r.authenticatorData),
      clientDataJSON: bufToB64url(r.clientDataJSON),
      signature: bufToB64url(r.signature),
      userHandle: r.userHandle ? bufToB64url(r.userHandle) : null,
    },
  };
}

/** Names the device a user agent spells, as the pair screen's Name field offers it. */
export function deviceNameFromUA(ua: string): string {
  if (ua.includes("iPhone")) return "iPhone";
  if (ua.includes("iPad")) return "iPad";
  if (ua.includes("Android")) return "Android";
  if (ua.includes("Macintosh") || ua.includes("Mac OS X")) return "Mac";
  if (ua.includes("Linux")) return "Linux";
  return "Browser";
}
