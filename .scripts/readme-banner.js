#!/usr/bin/env node
/* Generates assets/banner.svg, the animated README banner (designed in designs/roomr.pen, frame
   "Banner"). GitHub renders README SVGs inside <img>, so no web fonts and no scripts: text is
   outlined with Poppins and Roboto Mono (one <path> per glyph in <defs>, placed with <use>), and the
   motion is CSS keyframes inside the SVG, the same approach as fonoster/routr and fonoster/fonoster.

   Usage: node .scripts/readme-banner.js <fonts dir> [out file]
   The fonts dir needs Poppins-SemiBold.ttf, Poppins-Regular.ttf and RobotoMono[wght].ttf
   (OFL, from github.com/google/fonts). opentype.js must be resolvable (npm i opentype.js), or set
   OPENTYPE_PATH to its folder. whale-path.txt is the whale emoji outlined from Pencil. */
const fs = require("fs");
const path = require("path");
const opentype = require(process.env.OPENTYPE_PATH || "opentype.js");

const fontsDir = process.argv[2];
const out = process.argv[3] || path.join(__dirname, "..", "assets", "banner.svg");
if (!fontsDir) {
  console.error("usage: readme-banner.js <fonts dir> [out file]");
  process.exit(1);
}
const F = {
  semi: ["s", opentype.loadSync(path.join(fontsDir, "Poppins-SemiBold.ttf"))],
  reg: ["r", opentype.loadSync(path.join(fontsDir, "Poppins-Regular.ttf"))],
  mono: ["m", opentype.loadSync(path.join(fontsDir, "RobotoMono[wght].ttf"))]
};
const WHALE = fs.readFileSync(path.join(__dirname, "whale-path.txt"), "utf8").trim();

const W = 1280, H = 460;
const C = {
  bg: "#181818", raised: "#1E1E1E", surface: "#252525", border: "#333333",
  t1: "#FFFFFF", t3: "#C2C2C2", t4: "#8D8D8D", accent: "#FF9965"
};
const r1 = (n) => Math.round(n * 10) / 10;

/* ---------- Text as glyph <use>s ---------- */
const glyphDefs = new Map();
const text = (font, str, x, y, size, opts = {}) => {
  const [tag, f] = font;
  const scale = size / f.unitsPerEm;
  const ls = (opts.ls || 0) * size;
  const uses = [];
  let cx = x;
  const glyphs = f.stringToGlyphs(str);
  for (let i = 0; i < glyphs.length; i++) {
    const g = glyphs[i];
    if (g.index !== 0 && str[i] !== " ") {
      const id = `${tag}${g.index}`;
      if (!glyphDefs.has(id)) glyphDefs.set(id, g.getPath(0, 0, f.unitsPerEm).toPathData(1));
      uses.push(`<use href="#${id}" transform="translate(${r1(cx)} ${r1(y)}) scale(${scale.toFixed(5)})"/>`);
    }
    let adv = g.advanceWidth * scale;
    if (i < glyphs.length - 1) adv += f.getKerningValue(g, glyphs[i + 1]) * scale;
    cx += adv + ls;
  }
  return { svg: uses.join(""), w: cx - x };
};
const tp = (font, str, x, y, size, fill, opts = {}) => {
  const t = text(font, str, x, y, size, opts);
  return { svg: `<g fill="${fill}">${t.svg}</g>`, w: t.w };
};

const body = [];
const push = (s) => body.push(s);

/* ---------- Card, dot grid, light sweep (same construction as routr's banner) ---------- */
push(`<g clip-path="url(#card)">
<rect width="${W}" height="${H}" fill="${C.bg}"/>
<g mask="url(#atmos)"><rect width="${W}" height="${H}" fill="url(#dots)"/><rect width="${W}" height="${H}" fill="url(#dots-hi)" mask="url(#band)"/></g>
</g>`);

/* ---------- Logo: Poppins SemiBold wordmark and an accent dot on the baseline ---------- */
const word = tp(F.semi, "roomr", 64, 99.5, 44, C.t1, { ls: -0.0227 });
push(word.svg);
push(`<circle cx="${r1(64 + word.w + 7.5)}" cy="95.2" r="4.5" fill="${C.accent}"/>`);

/* ---------- Copy ---------- */
push(tp(F.semi, "Real-time audio", 64, 202.3, 52, C.t1, { ls: -0.0288 }).svg);
push(tp(F.semi, "streams for voice AI.", 64, 258.5, 52, C.t1, { ls: -0.0288 }).svg);
push(tp(F.reg, "Calls become rooms, and rooms become streams your", 64, 310.4, 19, C.t3).svg);
push(tp(F.reg, "app can read and write. One small Go node.", 64, 338, 19, C.t3).svg);
push(tp(F.mono, "github.com/fonoster/roomr", 64, 401.7, 15, C.t4).svg);

/* ---------- Container panel ---------- */
const PX = 675, PY = 49, PW = 545, PH = 362;
push(`<rect x="${PX}" y="${PY}" width="${PW}" height="${PH}" rx="16" fill="${C.raised}" stroke="${C.border}"/>`);
push(`<line x1="${PX}" y1="${PY + 52.5}" x2="${PX + PW}" y2="${PY + 52.5}" stroke="${C.border}"/>`);
push(`<path transform="translate(693 64)" fill="${C.accent}" d="${WHALE}"/>`);
push(tp(F.mono, "fonoster/roomr:0.1.0", 726, 79.6, 13, C.t3).svg);
push(`<circle class="live" cx="1145.5" cy="75" r="3.5" fill="${C.accent}"/>`);
push(tp(F.mono, "running", 1156, 79.6, 12, C.t4).svg);

/* Participants. Each row joins in turn, then its level bars move. */
const ICONS = {
  phone: `<path d="M13.832 16.568a1 1 0 0 0 1.213-.303l.355-.465A2 2 0 0 1 17 15h3a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2A18 18 0 0 1 2 4a2 2 0 0 1 2-2h3a2 2 0 0 1 2 2v3a2 2 0 0 1-.8 1.6l-.468.351a1 1 0 0 0-.292 1.233 14 14 0 0 0 6.392 6.384"/>`,
  bot: `<path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2M20 14h2M15 13v2M9 13v2"/>`,
  headphones: `<path d="M3 14h3a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-7a9 9 0 0 1 18 0v7a2 2 0 0 1-2 2h-1a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2h3"/>`
};
const people = [
  { icon: "phone", name: "Caller", tag: "PSTN · Opus", y: 126, hot: true, bars: [[155.5, 10], [151.5, 18], [147.5, 26], [153.5, 14], [149.5, 22], [145.5, 30], [152.5, 16], [148.5, 24], [154.5, 12], [150.5, 20]], cls: "b-hot" },
  { icon: "bot", name: "Your app", tag: "Session.Connect · RTP L16", y: 207, bars: [[238.5, 6], [236.5, 10], [237.5, 8], [234.5, 14], [237, 9], [235.5, 12], [238, 7], [236, 11], [237.5, 8], [238.5, 6]], cls: "b-app" },
  { icon: "headphones", name: "Supervisor", tag: "listener · receive only", y: 288, bars: Array.from({ length: 10 }, () => [320, 5]), cls: "b-still" }
];
people.forEach((p, i) => {
  const fill = p.hot ? `fill="${C.accent}" fill-opacity=".14" stroke="${C.accent}"` : `fill="${C.surface}" stroke="${C.border}"`;
  const iconY = p.y + 24.5;
  const bars = p.bars.map(([y, h], k) =>
    `<rect class="bar ${p.cls} d${k}" x="${1123 + k * 6}" y="${y}" width="3" height="${h}" rx="1.5" fill="${p.hot ? C.accent : C.t4}"/>`).join("");
  push(`<g class="join j${i}">
<rect x="699.5" y="${p.y + 0.5}" width="496" height="68" rx="10" ${fill}/>
<g transform="translate(715 ${iconY}) scale(.8333)" fill="none" stroke="${p.hot ? C.accent : C.t3}" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${ICONS[p.icon]}</g>
${tp(F.semi, p.name, 749, p.y + 30.8, 16, C.t1).svg}
${tp(F.mono, p.tag, 749, p.y + 51.6, 12, C.t4).svg}
${bars}
</g>`);
});
push(`<g class="join j3">${tp(F.mono, "mix-minus · 16 kHz · everyone hears everyone but themselves", 699, 382.7, 12, C.t4).svg}</g>`);

/* ---------- Motion (CSS keyframes) ----------
   One cycle: the caller joins, then the app, then the supervisor, then the mix-minus note, then a
   hold. Bars move after a row joins. prefers-reduced-motion shows the end state. */
const JOIN = [0.3, 1.1, 1.9, 2.7], HOLD = 4.2, T = JOIN[3] + 0.4 + HOLD;
const pct = (s) => ((s / T) * 100).toFixed(3);
let css = `
.sweep { transform: translateX(-200px); }
.join { opacity: 0 }
.bar { transform-box: fill-box; transform-origin: center }
@media (prefers-reduced-motion: reduce) { .join { opacity: 1 } }
@media (prefers-reduced-motion: no-preference) {
.sweep { animation: sweep 8s cubic-bezier(.65,0,.35,1) .6s infinite; }
@keyframes sweep { 0% { transform: translateX(0) } 40%, 100% { transform: translateX(${W + 380}px) } }
.live { animation: live 2.2s ease-in-out infinite }
@keyframes live { 0%, 100% { opacity: 1 } 50% { opacity: .35 } }
.b-hot { animation: lvl 1.1s ease-in-out infinite }
.b-app { animation: lvl 1.5s ease-in-out infinite }
@keyframes lvl { 0%, 100% { transform: scaleY(.35) } 50% { transform: scaleY(1) } }
.d1 { animation-delay: -.15s } .d2 { animation-delay: -.55s } .d3 { animation-delay: -.3s } .d4 { animation-delay: -.8s }
.d5 { animation-delay: -.05s } .d6 { animation-delay: -.65s } .d7 { animation-delay: -.4s } .d8 { animation-delay: -.9s } .d9 { animation-delay: -.2s }
`;
JOIN.forEach((t0, i) => {
  css += `.j${i} { animation: j${i} ${T}s linear infinite; }
@keyframes j${i} { 0% { opacity: 0 } ${pct(t0)}% { opacity: 0 } ${pct(t0 + 0.35)}% { opacity: 1 } ${pct(T - 0.4)}% { opacity: 1 } 100% { opacity: 0 } }
`;
});
css += `}\n`;

const defs = `<defs>
<clipPath id="card"><rect width="${W}" height="${H}" rx="28"/></clipPath>
<pattern id="dots" width="28" height="28" patternUnits="userSpaceOnUse"><circle cx="14" cy="14" r="1.35" fill="#fff" fill-opacity=".13"/></pattern>
<pattern id="dots-hi" width="28" height="28" patternUnits="userSpaceOnUse"><circle cx="14" cy="14" r="1.6" fill="#fff" fill-opacity=".6"/></pattern>
<radialGradient id="atmos-g" gradientUnits="userSpaceOnUse" cx="920" cy="193" r="832" gradientTransform="translate(920 193) scale(1 0.387) translate(-920 -193)"><stop offset="0" stop-color="#fff"/><stop offset=".72" stop-color="#fff" stop-opacity="0"/></radialGradient>
<mask id="atmos" maskUnits="userSpaceOnUse" x="0" y="0" width="${W}" height="${H}"><rect width="${W}" height="${H}" fill="url(#atmos-g)"/></mask>
<linearGradient id="band-g"><stop offset="0" stop-color="#fff" stop-opacity="0"/><stop offset=".32" stop-color="#fff" stop-opacity=".35"/><stop offset=".5" stop-color="#fff"/><stop offset=".68" stop-color="#fff" stop-opacity=".35"/><stop offset="1" stop-color="#fff" stop-opacity="0"/></linearGradient>
<mask id="band" maskUnits="userSpaceOnUse" x="0" y="0" width="${W}" height="${H}"><g class="sweep"><rect x="-180" y="-40" width="180" height="540" fill="url(#band-g)" transform="skewX(-10)"/></g></mask>
${Array.from(glyphDefs, ([id, d]) => `<path id="${id}" d="${d}"/>`).join("\n")}
</defs>`;

const doc = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" role="img" aria-labelledby="t d">
<title id="t">Roomr: real-time audio streams for voice AI.</title>
<desc id="d">Calls become rooms, and rooms become streams your app can read and write. A container panel shows a room with a caller, an app and a supervisor joining in turn, each with a moving audio level.</desc>
<style>${css}</style>
${defs}
${body.join("\n")}
</svg>
`;
fs.mkdirSync(path.dirname(out), { recursive: true });
fs.writeFileSync(out, doc);
console.log(`wrote ${out} (${(doc.length / 1024).toFixed(1)} KB, ${glyphDefs.size} glyphs, cycle ${T.toFixed(2)}s)`);
