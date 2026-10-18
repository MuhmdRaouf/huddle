// tls.ts — the e2e run's own certificate authority: one CA under the scratch folder and the
// leaf it signs for the hub's reserved name, both minted with the system's openssl (node's
// crypto reads certificates but cannot write one, and no dependency may join for this). The
// folder sits beside the run folder the global setup empties, not inside it, because the
// playwright config must know the CA's key hash before any browser launches — one folder both
// read, so the config's pin and the setup's import always name the same pair. Trust stays a
// pin, never a blanket ignore: the hub serves this CA behind its leaf, and Chromium's
// --ignore-certificate-errors-spki-list carries the CA's SPKI hash alone, so a chain this CA
// did not sign still fails (net::ERR_CERT_AUTHORITY_INVALID).

import { execFileSync } from "node:child_process";
import { createHash, X509Certificate } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { repoRoot } from "./paths.ts";

/** The hub's reserved name in the run: nothing on the machine resolves or serves it, so the
 *  browser reaches the hub only through the mapping the config passes Chromium. */
export const HUB_HOST = "hub.example.test";

/** Where the run's CA and leaf live: under the gitignored scratch, outside the folder the
 *  global setup empties every run. */
const caDir = path.join(repoRoot, ".agents", "scratch", "e2e", "ca");

/** The pair the setup imports: the chain the hub serves (leaf first, the CA behind it), the
 *  leaf's key, and the CA alone for clients that anchor trust there. */
export type HubPair = { chain: string; key: string; ca: string };

/** The CA's certificate, its key, the leaf's chain and the leaf's key, each a path. */
type CertFiles = { caCert: string; caKey: string; chain: string; key: string };

/** The four files in their folder. */
function files(): CertFiles {
  return {
    caCert: path.join(caDir, "ca.crt"),
    caKey: path.join(caDir, "ca.key"),
    chain: path.join(caDir, "chain.pem"),
    key: path.join(caDir, "hub.key"),
  };
}

/** Runs openssl once, any output it writes going to its files. */
function openssl(args: string[]): void {
  execFileSync("openssl", args, { cwd: caDir, stdio: ["ignore", "ignore", "inherit"] });
}

/** Writes one file at 0600 — a private key never lands world-readable. */
function writePrivate(file: string, body: string): void {
  fs.writeFileSync(file, body, { mode: 0o600 });
}

/** Mints the CA once: an EC P-256 root that only signs, living ten years — the pin outlives
 *  many runs. */
function mintCa(f: CertFiles): void {
  openssl(["ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", f.caKey]);
  fs.chmodSync(f.caKey, 0o600);
  openssl([
    "req", "-x509", "-new", "-key", f.caKey, "-subj", "/CN=Huddle E2E CA", "-days", "3650",
    "-out", f.caCert,
    "-addext", "basicConstraints=critical,CA:TRUE",
    "-addext", "keyUsage=critical,keyCertSign,cRLSign",
  ]);
}

/** Mints the leaf the hub serves for its name (the loopback address named too, so a client
 *  reaching the listener by IP handshakes as well), signed with SHA-256 — LibreSSL's default
 *  digest for `x509 -req` is SHA-1, which strict clients refuse. */
function mintLeaf(f: CertFiles): void {
  const csr = path.join(caDir, "hub.csr");
  const ext = path.join(caDir, "leaf.ext");
  openssl(["ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", f.key]);
  fs.chmodSync(f.key, 0o600);
  openssl(["req", "-new", "-key", f.key, "-subj", `/CN=${HUB_HOST}`, "-out", csr]);
  writePrivate(
    ext,
    `subjectAltName=DNS:${HUB_HOST},IP:127.0.0.1\n` +
      "basicConstraints=CA:FALSE\nkeyUsage=digitalSignature\nextendedKeyUsage=serverAuth\n",
  );
  openssl([
    "x509", "-req", "-in", csr, "-CA", f.caCert, "-CAkey", f.caKey, "-CAcreateserial",
    "-days", "365", "-sha256", "-out", path.join(caDir, "hub.crt"), "-extfile", ext,
  ]);
  // the chain the hub serves carries the CA behind the leaf, so the pin on the CA's key has a
  // certificate to match against
  fs.writeFileSync(
    f.chain,
    `${fs.readFileSync(path.join(caDir, "hub.crt"), "utf8")}${fs.readFileSync(f.caCert, "utf8")}`,
  );
}

/** True while the leaf is live another day at least; a spent leaf is re-signed by the same CA,
 *  so the pin the browsers launched with keeps its meaning. */
function leafLive(f: CertFiles): boolean {
  try {
    return new X509Certificate(fs.readFileSync(path.join(caDir, "hub.crt"), "utf8")).validToDate >
      new Date(Date.now() + 24 * 3600_000);
  } catch {
    return false;
  }
}

/** Answers the pair the setup imports, minting whatever is missing. */
export function hubPair(): HubPair {
  const f = files();
  fs.mkdirSync(caDir, { recursive: true });
  if (!fs.existsSync(f.caCert) || !fs.existsSync(f.caKey)) mintCa(f);
  if (!leafLive(f)) mintLeaf(f);
  return { chain: f.chain, key: f.key, ca: f.caCert };
}

/** The CA's SPKI hash the way Chromium's switch spells it: base64 of the SHA-256 over the
 *  DER-encoded SubjectPublicKeyInfo. The config calls this while evaluating, before any
 *  browser launches, so the CA is minted here when no earlier run left one. */
export function caSpkiHash(): string {
  hubPair();
  const f = files();
  const cert = new X509Certificate(fs.readFileSync(f.caCert, "utf8"));
  const spki = cert.publicKey.export({ type: "spki", format: "der" });
  return createHash("sha256").update(spki).digest("base64");
}

/** The switches every Chromium of the run launches with: the reserved name mapped onto
 *  loopback (no hosts file is ever touched), and the CA's key pinned — a pin, not a blanket
 *  certificate ignore. */
export function chromiumTlsArgs(): string[] {
  return [
    `--host-resolver-rules=MAP ${HUB_HOST} 127.0.0.1`,
    `--ignore-certificate-errors-spki-list=${caSpkiHash()}`,
  ];
}
