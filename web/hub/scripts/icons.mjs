#!/usr/bin/env node
// icons.mjs — the PWA's PNG icons, rasterised from assets/huddle.svg (the logo the dashboard's
// favicon and the hub's pages share): 192, 512, and a maskable 512 whose background runs to
// the edges so any launcher mask crops colour, not transparency. Pure Node over the SVG's own
// shapes (rects, circles, two-stop gradients), zlib for the IDAT: no network, no dependencies,
// and the same input gives the same bytes, so a fresh build never drifts from the committed
// icons. scripts/build.mjs calls iconFiles() and ships what it returns.
import { readFileSync } from "node:fs";
import { deflateSync } from "node:zlib";

const SVG = new URL("../assets/huddle.svg", import.meta.url);
const VIEW = 128; // the logo's viewBox; every icon is the whole logo at its own scale

/** One attribute of an element's opening tag, as a string. */
const attr = (tag, name) => tag.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1];

/** One numeric attribute, with a default. */
const num = (tag, name, dflt = 0) => {
  const v = attr(tag, name);
  return v === undefined ? dflt : Number(v);
};

/** #rrggbb as [r, g, b]. */
const rgb = (hex) => [
  Number.parseInt(hex.slice(1, 3), 16),
  Number.parseInt(hex.slice(3, 5), 16),
  Number.parseInt(hex.slice(5, 7), 16),
];

/** The linear gradients the logo references, by id: corner-to-corner, two stops. */
function gradients(svg) {
  const out = new Map();
  for (const block of svg.match(/<linearGradient[\s\S]*?<\/linearGradient>/g) ?? []) {
    out.set(attr(block, "id") ?? "", {
      x1: num(block, "x1", 0),
      y1: num(block, "y1", 0),
      x2: num(block, "x2", 1),
      y2: num(block, "y2", 1),
      stops: [...block.matchAll(/stop-color="([^"]+)"/g)].map((m) => rgb(m[1])),
    });
  }
  return out;
}

/** The logo's shapes, in paint order: rounded rects and circles with fill and stroke. */
function shapes(svg) {
  const body = svg.replaceAll(/<!--[\s\S]*?-->/g, "");
  return [...body.matchAll(/<(rect|circle)\b[^>]*\/>/g)].map((m) => ({
    kind: m[1],
    x: num(m[0], "x"),
    y: num(m[0], "y"),
    w: num(m[0], "width"),
    h: num(m[0], "height"),
    r: num(m[0], "rx"),
    cx: num(m[0], "cx"),
    cy: num(m[0], "cy"),
    radius: num(m[0], "r"),
    fill: attr(m[0], "fill") ?? "none",
    stroke: attr(m[0], "stroke") ?? "none",
    strokeWidth: num(m[0], "stroke-width", 1),
    dash: attr(m[0], "stroke-dasharray")
      ?.split(/\s+/)
      .map((n) => Number(n)),
    opacity: num(m[0], "opacity", 1),
  }));
}

/** Signed distance to a rounded rectangle's edge; negative inside. */
function sdRect(u, v, s) {
  const qx = Math.abs(u - (s.x + s.w / 2)) - s.w / 2 + s.r;
  const qy = Math.abs(v - (s.y + s.h / 2)) - s.h / 2 + s.r;
  return Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - s.r;
}

/** One shape as a painter: paints its colour at a sample, with its opacity, or misses it. */
function painter(shape, grads, maskable) {
  let s = shape;
  if (s.kind === "rect" && maskable) {
    // the maskable background runs to the edges (any mask crops colour, not transparency) and
    // the border's rounded outline goes with the shape it traced
    if (s.fill === "none") return null;
    s = { ...shape, x: 0, y: 0, w: VIEW, h: VIEW, r: 0 };
  }

  // a colour as a painter over the shape's own bounding box (SVG gradients are bbox-relative)
  const colored = (paint) => {
    if (paint === "none" || paint === undefined) return null;
    if (!paint.startsWith("url(")) {
      const [r, g, b] = rgb(paint);
      return () => [r, g, b, 1];
    }
    const grad = grads.get(paint.slice(5, -1));
    const box =
      s.kind === "rect"
        ? { x: s.x, y: s.y, w: s.w, h: s.h }
        : { x: s.cx - s.radius, y: s.cy - s.radius, w: s.radius * 2, h: s.radius * 2 };
    const p1x = box.x + grad.x1 * box.w;
    const p1y = box.y + grad.y1 * box.h;
    const dx = box.x + grad.x2 * box.w - p1x;
    const dy = box.y + grad.y2 * box.h - p1y;
    const len = dx * dx + dy * dy || 1;
    const [a, b] = grad.stops;
    return (u, v) => {
      let t = ((u - p1x) * dx + (v - p1y) * dy) / len;
      t = t < 0 ? 0 : t > 1 ? 1 : t;
      return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t, 1];
    };
  };

  const fill = colored(s.fill);
  const stroke = colored(s.stroke);
  const half = s.strokeWidth / 2;
  const period = s.dash ? s.dash[0] + s.dash[1] : 0;
  const dashOn = s.dash ? s.dash[0] : 0;

  const paint = (u, v) => {
    const d = s.kind === "rect" ? sdRect(u, v, s) : Math.hypot(u - s.cx, v - s.cy) - s.radius;
    if (stroke && Math.abs(d) <= half) {
      // a dashed stroke covers only its dashes, the caps rounded by half the stroke's width
      if (s.dash) {
        const m = (((Math.atan2(v - s.cy, u - s.cx) + 2 * Math.PI) % (2 * Math.PI)) * s.radius) % period;
        if (m > dashOn + half && m < period - half) {
          if (fill && d < 0) return fill(u, v);
          return null;
        }
      }
      return stroke(u, v);
    }
    if (fill && d < 0) return fill(u, v);
    return null;
  };
  paint.opacity = s.opacity;
  return paint;
}

/** Rasterises the logo at `size` square, `maskable` for the full-bleed variant. */
function rasterize(size, maskable, specs, grads) {
  const N = 4; // 4x4 supersamples per pixel carry the anti-aliasing
  const step = VIEW / size;
  const layer = specs.map((s) => painter(s, grads, maskable)).filter((p) => p !== null);
  const px = new Uint8Array(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let red = 0;
      let green = 0;
      let blue = 0;
      let alpha = 0; // premultiplied, summed over the samples
      for (let sy = 0; sy < N; sy++) {
        for (let sx = 0; sx < N; sx++) {
          const u = (x + (sx + 0.5) / N) * step;
          const v = (y + (sy + 0.5) / N) * step;
          let r = 0;
          let g = 0;
          let b = 0;
          let a = 0;
          for (const paint of layer) {
            const col = paint(u, v);
            if (!col) continue;
            const cover = col[3] * (paint.opacity ?? 1);
            if (cover <= 0) continue;
            const na = cover + a * (1 - cover);
            r = (col[0] * cover + r * a * (1 - cover)) / na;
            g = (col[1] * cover + g * a * (1 - cover)) / na;
            b = (col[2] * cover + b * a * (1 - cover)) / na;
            a = na;
          }
          red += r * a;
          green += g * a;
          blue += b * a;
          alpha += a;
        }
      }
      const i = (y * size + x) * 4;
      px[i] = alpha > 0 ? Math.round(red / alpha) : 0;
      px[i + 1] = alpha > 0 ? Math.round(green / alpha) : 0;
      px[i + 2] = alpha > 0 ? Math.round(blue / alpha) : 0;
      px[i + 3] = Math.round((alpha / (N * N)) * 255);
    }
  }
  return px;
}

const CRC = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

/** One PNG chunk: length, type, data, CRC. */
function chunk(type, data) {
  const out = Buffer.alloc(data.length + 12);
  out.writeUInt32BE(data.length, 0);
  out.write(type, 4, "ascii");
  data.copy(out, 8);
  let crc = 0xffffffff;
  for (const byte of out.subarray(4, 8 + data.length)) crc = CRC[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  out.writeUInt32BE((crc ^ 0xffffffff) >>> 0, 8 + data.length);
  return out;
}

/** An RGBA PNG. */
function png(size, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  const row = size * 4 + 1;
  const raw = Buffer.alloc(row * size);
  for (let y = 0; y < size; y++) {
    raw[y * row] = 0; // filter: none
    Buffer.from(rgba.buffer, rgba.byteOffset + y * size * 4, size * 4).copy(raw, y * row + 1);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

/** The PWA's icons, as build.mjs ships them into the embedded dashboard. */
export function iconFiles() {
  const svg = readFileSync(SVG, "utf8");
  const specs = shapes(svg);
  const grads = gradients(svg);
  const icon = (name, size, maskable) => ({
    fileName: `icons/${name}.png`,
    contents: png(size, rasterize(size, maskable, specs, grads)),
  });
  return [icon("icon-192", 192, false), icon("icon-512", 512, false), icon("icon-maskable-512", 512, true)];
}
