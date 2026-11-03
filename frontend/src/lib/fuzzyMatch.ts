// Forgiving search for pickers: words may come in any order, punctuation and
// spacing are ignored, and a word with a small typo still matches.

const normalize = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, " ").trim();

// editDistance is the Levenshtein distance, stopping early past max.
function editDistance(a: string, b: string, max: number): number {
  if (Math.abs(a.length - b.length) > max) return max + 1;
  let prev = Array.from({ length: b.length + 1 }, (_, i) => i);
  for (let i = 1; i <= a.length; i++) {
    const cur = [i];
    let rowMin = i;
    for (let j = 1; j <= b.length; j++) {
      const v = Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1));
      cur.push(v);
      rowMin = Math.min(rowMin, v);
    }
    if (rowMin > max) return max + 1;
    prev = cur;
  }
  return prev[b.length];
}

function isSubsequence(word: string, text: string): boolean {
  let i = 0;
  for (const ch of text) if (ch === word[i] && ++i === word.length) return true;
  return false;
}

// wordScore rates how well one query word matches; 0 is no match.
function wordScore(word: string, tokens: string[], compact: string): number {
  if (tokens.some((t) => t.startsWith(word))) return 4;
  if (compact.includes(word)) return 3;
  if (word.length >= 4) {
    const max = word.length >= 7 ? 2 : 1;
    // Against whole tokens and same-length prefixes, so "dashcope" finds
    // "dashscope" and "openrout" finds "openrouter".
    if (tokens.some((t) => editDistance(word, t, max) <= max || editDistance(word, t.slice(0, word.length), max) <= max)) return 2;
    if (isSubsequence(word, compact)) return 1;
  }
  return 0;
}

// fuzzyScore scores text against query: 0 when any query word fails to
// match, higher for closer matches. An empty query matches everything with 1.
export function fuzzyScore(query: string, text: string): number {
  const words = normalize(query).split(" ").filter(Boolean);
  if (words.length === 0) return 1;
  const tokens = normalize(text).split(" ").filter(Boolean);
  const compact = tokens.join("");
  let total = 0;
  for (const w of words) {
    const s = wordScore(w, tokens, compact);
    if (s === 0) return 0;
    total += s;
  }
  return total;
}

// fuzzyFilter keeps the items that match query, best first; ties keep their
// original order.
export function fuzzyFilter<T>(items: T[], query: string, text: (item: T) => string): T[] {
  if (!normalize(query)) return items;
  return items
    .map((item, i) => ({ item, i, score: fuzzyScore(query, text(item)) }))
    .filter((r) => r.score > 0)
    .sort((a, b) => b.score - a.score || a.i - b.i)
    .map((r) => r.item);
}
