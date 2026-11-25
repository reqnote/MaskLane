const automaticPattern = /<pl:(?:company|project|person|confidential|other):[0-9]+>/;

function literalPattern(keys) {
  const aliases = [...keys].sort((a, b) => b.length - a.length);
  const wholeTokens = [];
  const literals = [];
  for (const alias of aliases) {
    const escaped = alias.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    // Keep unknown tokens intact even when a short literal such as "pl" is registered.
    const patterns = alias.search(automaticPattern) === 0 ? wholeTokens : literals;
    patterns.push(escaped);
  }
  return new RegExp([...wholeTokens, automaticPattern.source, ...literals].join("|"), "g");
}

export function maskText(input, rules, previousNumbers = {}) {
  const indices = new Map(rules.map((rule, index) => [rule.original, index]));
  const reserved = new Set(input.match(new RegExp(automaticPattern.source, "g")));
  for (const rule of rules) reserved.add(rule.replacement);
  const numbers = { ...previousNumbers };
  const automatic = {};
  let masked = 0;
  const text = input.replace(literalPattern(indices.keys()), (original) => {
    const index = indices.get(original);
    if (index === undefined) return original;
    const { replacement, kind } = rules[index];
    masked++;
    if (replacement) return replacement;
    if (!automatic[index]) {
      let number = numbers[kind] || 0;
      let token;
      do {
        token = "<pl:" + kind + ":" + (++number) + ">";
      } while (reserved.has(token));
      numbers[kind] = number;
      automatic[index] = token;
    }
    return automatic[index];
  });
  return { text, masked, automatic, numbers };
}

// Process the input once so a restored original can never trigger a second replacement.
export function restoreText(input, rules, automatic = {}) {
  const originals = new Map();
  for (const [index, rule] of rules.entries()) {
    const alias = rule.replacement || automatic[index];
    if (!alias) continue;
    originals.set(alias, originals.has(alias) ? null : rule.original);
  }
  let restored = 0;
  let unknown = 0;
  const ambiguous = new Set();
  const text = input.replace(literalPattern(originals.keys()), (alias) => {
    const original = originals.get(alias);
    if (original == null) {
      if (original === null) ambiguous.add(alias);
      else unknown++;
      return alias;
    }
    restored++;
    return original;
  });
  return { text, restored, unknown, ambiguous: [...ambiguous] };
}
