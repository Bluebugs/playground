// WAT highlighter.
// Tier-2: load-bearing SPMD storytelling ops (shuffles for table lookup).
// Tier-1: broad SIMD opcode family (v128.*, i*x*.*, f*x*.*).
// Strategy: walk the input once, scanning for tier-2 matches first at each
// position (longest/most specific wins), then tier-1, then plain text. This
// avoids the priority/escape headaches of regex chaining.

const TIER2 = new Set([
  'i8x16.swizzle',
  'i8x16.shuffle',
  'i8x16.relaxed_swizzle',
]);

const TIER1_TYPE_RE = /^(v128|i8x16|i16x8|i32x4|i64x2|f32x4|f64x2|f16x8)\.([a-z_][a-z0-9_]*)/;

const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
function escapeHtml(s) {
  return s.replace(/[&<>"']/g, c => ESC[c]);
}

function isWordChar(c) {
  return c !== undefined && /[A-Za-z0-9_.]/.test(c);
}

export function highlightWAT(text) {
  let out = '';
  let i = 0;
  let buf = '';
  const flush = () => { if (buf) { out += escapeHtml(buf); buf = ''; } };

  while (i < text.length) {
    // Only attempt a match at a word boundary (previous char is not a word
    // char or part of the type token). For simplicity, attempt always; the
    // regex ^ anchor on the substring handles it.
    const rest = text.slice(i, i + 48); // window large enough for any opname (incl. future relaxed-simd / FP16)
    const prev = i > 0 ? text[i - 1] : ' ';
    if (!isWordChar(prev)) {
      const m = TIER1_TYPE_RE.exec(rest);
      if (m) {
        const full = m[0];
        const after = text[i + full.length];
        if (!isWordChar(after)) {
          flush();
          if (TIER2.has(full)) {
            out += `<span class="hi-tier2">${escapeHtml(full)}</span>`;
          } else {
            out += `<span class="hi-tier1">${escapeHtml(full)}</span>`;
          }
          i += full.length;
          continue;
        }
      }
    }
    buf += text[i];
    i++;
  }
  flush();
  return out;
}
