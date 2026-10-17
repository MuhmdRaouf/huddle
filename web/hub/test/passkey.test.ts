// passkey.test.ts — the WebAuthn glue: base64url in and out of ArrayBuffers (both directions,
// with bytes past 127), the server's creation and assertion options decoded into the browser's
// own shapes (the buffer fields decoded, the rest passed through, the credential lists decoded
// field by field), the browser's credential of either ceremony encoded back into the JSON the
// finish routes speak (a null userHandle stays null), and the device name a user agent spells.

import { describe, expect, it } from "vite-plus/test";
import {
  assertionFromJSON,
  b64urlToBuf,
  bufToB64url,
  creationFromJSON,
  credentialToJSON,
  deviceNameFromUA,
} from "../src/passkey.ts";

/** One ArrayBuffer over plain bytes. */
const bytes = (...b: number[]): ArrayBuffer => new Uint8Array(b).buffer;

/** Reads one buffer's bytes back, whichever buffer shape the DOM named it. */
const raw = (b: ArrayBuffer | ArrayBufferView): number[] => [
  ...new Uint8Array(b instanceof ArrayBuffer ? b : (b.buffer as ArrayBuffer)),
];

/** A fake browser credential of the attestation shape, as narrow as the encoder reads. */
const attestation = (over: Record<string, unknown> = {}): PublicKeyCredential =>
  ({
    id: "cred-1",
    rawId: bytes(1, 2, 3),
    type: "public-key",
    response: {
      attestationObject: bytes(9, 9),
      clientDataJSON: bytes(7),
      ...over,
    },
  }) as unknown as PublicKeyCredential;

/** A fake browser credential of the assertion shape. */
const assertion = (over: Record<string, unknown> = {}): PublicKeyCredential =>
  ({
    id: "cred-2",
    rawId: bytes(4, 5),
    type: "public-key",
    response: {
      authenticatorData: bytes(8),
      clientDataJSON: bytes(7),
      signature: bytes(6, 6),
      userHandle: bytes(3, 3),
      ...over,
    },
  }) as unknown as PublicKeyCredential;

describe("base64url buffers", () => {
  it("encodes bytes without padding, and reads them back", () => {
    expect(bufToB64url(bytes(104, 105))).toBe("aGk");
    expect(bufToB64url(bytes(246))).toBe("9g");
    // 0xff twice writes the padded alphabet's / /, which base64url spells _ _
    expect(bufToB64url(bytes(0, 255, 255))).toBe("AP__");
    expect(raw(b64urlToBuf("aGk"))).toEqual([104, 105]);
    expect(raw(b64urlToBuf("9g"))).toEqual([246]);
    expect(raw(b64urlToBuf("AP__"))).toEqual([0, 255, 255]);
    // padding is tolerated: the same bytes the unpadded spelling names (a vector whose pad
    // bits are zero keeps the assertion independent of happy-dom's atob, which reads them as ones)
    expect(raw(b64urlToBuf("aGk="))).toEqual([104, 105]);
    // bytes that would write + and / round-trip through the url alphabet alone
    const special = bytes(0, 255, 255, 251, 62);
    const url = bufToB64url(special);
    expect(url.includes("+")).toBe(false);
    expect(url.includes("/")).toBe(false);
    expect(raw(b64urlToBuf(url))).toEqual(raw(special));
  });

  it("round-trips both ways", () => {
    const b = bytes(0, 1, 2, 250, 251, 252, 253, 254, 255);
    expect(raw(b64urlToBuf(bufToB64url(b)))).toEqual(raw(b));
    expect(bufToB64url(b64urlToBuf("Y2hhbGxlbmdl"))).toBe("Y2hhbGxlbmdl");
  });
});

describe("creationFromJSON", () => {
  it("decodes the challenge, the user id and every excluded credential id, and passes the rest through", () => {
    const o = creationFromJSON({
      publicKey: {
        rp: { name: "Huddle", id: "hub.mine.test" },
        user: { id: "dXNlcg", name: "owner", displayName: "Owner" },
        challenge: "Y2hhbGxlbmdl",
        pubKeyCredParams: [{ type: "public-key", alg: -7 }],
        timeout: 60_000,
        authenticatorSelection: { userVerification: "required" },
        excludeCredentials: [{ type: "public-key", id: "a2V5" }],
      },
    });
    expect(raw(o.publicKey.challenge)).toEqual(raw(bytes(...new TextEncoder().encode("challenge"))));
    const user = o.publicKey.user as unknown as Record<string, unknown>;
    expect(raw(user.id as ArrayBuffer)).toEqual(raw(bytes(117, 115, 101, 114)));
    expect(user.name).toBe("owner");
    expect(o.publicKey.rp).toEqual({ name: "Huddle", id: "hub.mine.test" });
    expect(o.publicKey.pubKeyCredParams).toEqual([{ type: "public-key", alg: -7 }]);
    expect(o.publicKey.timeout).toBe(60_000);
    expect(o.publicKey.authenticatorSelection).toEqual({ userVerification: "required" });
    const excluded = o.publicKey.excludeCredentials ?? [];
    expect(excluded).toHaveLength(1);
    const first = excluded[0];
    expect(raw(first?.id ?? new ArrayBuffer(0))).toEqual(raw(bytes(107, 101, 121)));
    expect(first?.type).toBe("public-key");
  });

  it("leaves the credential lists out when the server named none", () => {
    const creation = creationFromJSON({
      publicKey: { challenge: "aGk", user: { id: "dXNlcg" } },
    });
    expect(creation.publicKey.excludeCredentials).toBeUndefined();
    const request = assertionFromJSON({ publicKey: { challenge: "aGk" } });
    expect(request.publicKey.allowCredentials).toBeUndefined();
    expect(raw(request.publicKey.challenge)).toEqual([104, 105]);
  });
});

describe("assertionFromJSON", () => {
  it("decodes the challenge and every allowed credential id", () => {
    const o = assertionFromJSON({
      publicKey: {
        challenge: "Y2hhbGxlbmdl",
        rpId: "hub.mine.test",
        userVerification: "required",
        allowCredentials: [{ type: "public-key", id: "a2V5" }],
      },
    });
    const challenge = o.publicKey.challenge;
    expect(raw(challenge)).toEqual(raw(b64urlToBuf("Y2hhbGxlbmdl")));
    expect(o.publicKey.rpId).toBe("hub.mine.test");
    expect(o.publicKey.userVerification).toBe("required");
    const allowed = o.publicKey.allowCredentials ?? [];
    expect(allowed).toHaveLength(1);
    const first = allowed[0];
    expect(raw(first?.id ?? new ArrayBuffer(0))).toEqual(raw(bytes(107, 101, 121)));
  });
});

describe("credentialToJSON", () => {
  it("encodes the attestation's buffers", () => {
    const out = credentialToJSON(attestation());
    expect(out).toEqual({
      id: "cred-1",
      rawId: "AQID",
      type: "public-key",
      response: { attestationObject: "CQk", clientDataJSON: "Bw" },
    });
  });

  it("encodes the assertion's buffers, and a null userHandle stays null", () => {
    const out = credentialToJSON(assertion());
    expect(out).toEqual({
      id: "cred-2",
      rawId: "BAU",
      type: "public-key",
      response: { authenticatorData: "CA", clientDataJSON: "Bw", signature: "BgY", userHandle: "AwM" },
    });
    const none = credentialToJSON(assertion({ userHandle: null }));
    expect(none.response).toEqual({
      authenticatorData: "CA",
      clientDataJSON: "Bw",
      signature: "BgY",
      userHandle: null,
    });
  });
});

describe("deviceNameFromUA", () => {
  it("names the device family the user agent spells", () => {
    expect(deviceNameFromUA("Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X)")).toBe("iPhone");
    expect(deviceNameFromUA("Mozilla/5.0 (iPad; CPU OS 16_6 like Mac OS X)")).toBe("iPad");
    expect(deviceNameFromUA("Mozilla/5.0 (Linux; Android 14; Pixel 8)")).toBe("Android");
    expect(deviceNameFromUA("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")).toBe("Mac");
    expect(deviceNameFromUA("Mozilla/5.0 (X11; Linux x86_64)")).toBe("Linux");
    expect(deviceNameFromUA("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBe("Browser");
    expect(deviceNameFromUA("")).toBe("Browser");
  });
});
