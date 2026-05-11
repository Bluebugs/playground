// x86-64 / AVX2 highlighter.
// Tier-2: load-bearing SPMD codegen storytelling ops (table lookups, pmadd
// chains, permutes, blends). These render bright + bold.
// Tier-1: broad SIMD mnemonic family (v* AVX, p* SSE, plus a small set of
// scalar SIMD-adjacent ops). These render in subtle blue.
// Anything else (mov, add, cmp, jmp, lea, push, pop, ret, labels, addresses,
// comments) stays uncolored.

const TIER2 = new Set([
  'vpshufb',
  'vpermd',
  'vpermps',
  'vpermq',
  'vpmaddubsw',
  'vpmaddwd',
  'vpblendvb',
  'vbroadcasti128',
  'vinserti128',
  'vextracti128',
  'vtestps',
  'vtestpd',
  'pshufb',
  'pmaddubsw',
  'pmaddwd',
  'pblendvb',
]);

// Heuristic: a SIMD mnemonic starts with 'v' (AVX) or 'p' (packed SSE) and
// is all lowercase letters/digits. We additionally accept a small list of
// scalar→packed conversion / FMA ops.
const SIMD_PREFIX_RE = /^[a-z][a-z0-9]*$/;

// Mnemonics that look like SIMD but aren't — keep this list small.
const TIER1_EXCLUDE = new Set([
  'push', 'pop', 'popcnt', 'popfq', 'pause', 'prefetch', 'prefetcht0',
  'prefetcht1', 'prefetcht2', 'prefetchnta',
  'ptr',
]);

function isSimdMnemonic(tok) {
  if (!SIMD_PREFIX_RE.test(tok)) return false;
  if (tok.length < 3) return false;
  if (TIER1_EXCLUDE.has(tok)) return false;
  // AVX: starts with 'v' followed by another letter.
  if (tok[0] === 'v' && /^v[a-z]/.test(tok)) return true;
  // SSE packed: starts with 'p' followed by another letter.
  if (tok[0] === 'p' && /^p[a-z]/.test(tok)) return true;
  return false;
}

const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
function escapeHtml(s) {
  return s.replace(/[&<>"']/g, c => ESC[c]);
}

// Match a single mnemonic token at the start of a line (after optional
// leading whitespace and an address column like "  4a3b21: ").
// objdump -d -M intel emits lines like:
//    4a3b21:  c5 fd 6f 05 ...  vmovdqa ymm0,YMMWORD PTR [rip+0x12345]
// Mnemonics are also the first token after the bytecode column.
//
// Rather than trying to lex the full objdump format, we just look for tokens
// at word boundaries that match isSimdMnemonic. False positives are unlikely
// because operand names are register names (rax, rbx, ymm0, xmm5) which
// don't pass the v*/p* prefix test (except 'pxor', 'pand' etc. which ARE
// SIMD ops, so highlighting them is correct).

export function highlightX86(text) {
  let out = '';
  // Process line by line so we can avoid highlighting our own banner comments
  // and section headers.
  const lines = text.split('\n');
  for (let li = 0; li < lines.length; li++) {
    const line = lines[li];
    if (li > 0) out += '\n';
    // Skip empty lines, section headers, function name lines, banners.
    if (line === '' ||
        line.startsWith('Disassembly of section') ||
        line.startsWith('// ') ||
        /^[0-9a-f]+ <[^>]+>:$/.test(line.trim())) {
      out += escapeHtml(line);
      continue;
    }
    out += highlightLine(line);
  }
  return out;
}

function highlightLine(line) {
  let out = '';
  let i = 0;
  const n = line.length;
  while (i < n) {
    // Find the start of the next identifier-like token.
    const c = line[i];
    if (!/[a-z]/.test(c)) {
      out += escapeHtml(c);
      i++;
      continue;
    }
    // Don't start mid-token: previous char must not be a token char.
    const prev = i > 0 ? line[i - 1] : ' ';
    if (/[A-Za-z0-9_]/.test(prev)) {
      out += escapeHtml(c);
      i++;
      continue;
    }
    // Scan to end of token.
    let j = i;
    while (j < n && /[a-z0-9]/.test(line[j])) j++;
    const tok = line.slice(i, j);
    if (TIER2.has(tok)) {
      out += `<span class="hi-tier2">${escapeHtml(tok)}</span>`;
    } else if (isSimdMnemonic(tok)) {
      out += `<span class="hi-tier1">${escapeHtml(tok)}</span>`;
    } else {
      out += escapeHtml(tok);
    }
    i = j;
  }
  return out;
}
