// @vitest-environment node
// pwa-icons.test.ts — the PNG icons scripts/icons.mjs rasterises from the logo: the Apple icon is a
// 180 px opaque square the logo fills (iOS rounds the corners itself and paints transparency black),
// so its art sits larger than in the maskable 512, whose art keeps inside the launcher safe zone.
// The generator is a Node build script, so this file runs in Node, not happy-dom.
import { inflateSync } from "node:zlib";
import { describe, expect, it } from "vite-plus/test";

type IconFile = { fileName: string; contents: Buffer };
type IconsModule = { iconFiles: () => IconFile[] };

// a plain Node module with no type declarations: loaded by URL and typed here at the edge
const icons = (await import(
  /* @vite-ignore */ new URL("../scripts/icons.mjs", import.meta.url).href
)) as IconsModule;
// one rasterisation of every icon serves the whole file: each costs about a second
const files = icons.iconFiles();

type Image = { width: number; height: number; rgba: Uint8Array };

/** Decodes the generator's own PNGs: 8-bit RGBA, one IDAT, every row filtered with "none". */
function decode(file: IconFile): Image {
  const png = file.contents;
  const width = png.readUInt32BE(16);
  const height = png.readUInt32BE(20);
  const idat = png.indexOf("IDAT", 8, "ascii");
  const raw = inflateSync(png.subarray(idat + 4, idat + 4 + png.readUInt32BE(idat - 4)));
  const rgba = new Uint8Array(width * height * 4);
  for (let y = 0; y < height; y++) {
    rgba.set(raw.subarray(y * (width * 4 + 1) + 1, (y + 1) * (width * 4 + 1)), y * width * 4);
  }
  return { width, height, rgba };
}

const pixel = (img: Image, x: number, y: number) => {
  const i = (y * img.width + x) * 4;
  return [...img.rgba.subarray(i, i + 4)];
};

/** The share of the width the top member's light-blue disc (#89b4fa) spans on the middle column. */
function memberSpan(img: Image): number {
  const x = Math.floor(img.width / 2);
  let rows = 0;
  for (let y = 0; y < img.height; y++) {
    const [r, g, b] = pixel(img, x, y);
    if (Math.abs((r ?? 0) - 0x89) < 12 && Math.abs((g ?? 0) - 0xb4) < 12 && Math.abs((b ?? 0) - 0xfa) < 12)
      rows++;
  }
  return rows / img.height;
}

const byName = (name: string): Image => {
  const file = files.find((f) => f.fileName === `icons/${name}.png`);
  if (!file) throw new Error(`iconFiles() ships no icons/${name}.png`);
  return decode(file);
};

describe("iconFiles", () => {
  it("ships the Apple icon as a 180 px PNG", () => {
    const apple = byName("icon-apple-180");
    expect([apple.width, apple.height]).toEqual([180, 180]);
  });

  it("paints the Apple icon opaque to its corners", () => {
    const apple = byName("icon-apple-180");
    for (const [x, y] of [
      [0, 0],
      [179, 0],
      [0, 179],
      [179, 179],
    ] as const) {
      expect(pixel(apple, x, y)[3]).toBe(255);
    }
  });

  it("draws the logo larger in the Apple icon than in the maskable 512", () => {
    expect(memberSpan(byName("icon-apple-180"))).toBeGreaterThan(memberSpan(byName("icon-maskable-512")));
  });

  it("ships the badge as a 96 px PNG", () => {
    const badge = byName("icon-badge-96");
    expect([badge.width, badge.height]).toEqual([96, 96]);
  });

  it("paints the badge white wherever the logo shows", () => {
    const badge = byName("icon-badge-96");
    for (let y = 0; y < badge.height; y++) {
      for (let x = 0; x < badge.width; x++) {
        const [r, g, b, a] = pixel(badge, x, y);
        if (a === 0) continue;
        expect([r, g, b], `pixel ${x},${y}`).toEqual([255, 255, 255]);
      }
    }
  });

  it("draws the badge as the logo's silhouette, not a filled square", () => {
    const badge = byName("icon-badge-96");
    let clear = 0;
    let solid = 0;
    for (let i = 3; i < badge.rgba.length; i += 4) {
      const a = badge.rgba[i];
      if (a === 0) clear++;
      if (a === 255) solid++;
    }
    expect(clear).toBeGreaterThan(0);
    expect(solid).toBeGreaterThan(0);
  });
});
