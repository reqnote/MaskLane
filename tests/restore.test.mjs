import assert from "node:assert/strict";
import test from "node:test";
import { maskText, restoreText } from "../overlay/cmd/masklane/web/restore.mjs";

test("masks literal keywords longest first and round-trips explicit and generated aliases", () => {
  const rules = [
    { kind: "company", original: "青葉", replacement: "" },
    { kind: "company", original: "青葉[A]+", replacement: "顧客$&" },
    { kind: "person", original: "田中", replacement: "" },
  ];
  const input = " 青葉[A]+ の田中\n青葉・田中 ";
  const result = maskText(input, rules);
  assert.equal(result.text, " 顧客$& の<pl:person:1>\n<pl:company:1>・<pl:person:1> ");
  assert.equal(result.masked, 4);
  assert.equal(restoreText(result.text, rules, result.automatic).text, input);
});

test("avoids token collisions with input, explicit aliases and earlier local masks", () => {
  const rules = [
    { kind: "person", original: "田中", replacement: "<pl:person:2>" },
    { kind: "person", original: "山田", replacement: "" },
    { kind: "other", original: "pl", replacement: "記号" },
  ];
  const first = maskText("<pl:person:1> 山田 田中 pl", rules);
  assert.equal(first.text, "<pl:person:1> <pl:person:3> <pl:person:2> 記号");
  const second = maskText("山田", rules, first.numbers);
  assert.equal(second.text, "<pl:person:4>");
  assert.equal(restoreText(first.text, rules, second.automatic).unknown, 2);
});

test("unmatched text, empty input and an empty dictionary produce no masks", () => {
  const rule = { kind: "other", original: "ABC", replacement: "案件" };
  for (const [input, rules] of [["abc", [rule]], ["", [rule]], ["そのまま", []]]) {
    assert.deepEqual(maskText(input, rules), {text: input, masked: 0, automatic: {}, numbers: {}});
  }
});

test("restores literal aliases longest first without changing whitespace or rescanning originals", () => {
  const rules = [
    { original: "青葉", replacement: "顧客" },
    { original: "顧客窓口 $&", replacement: "顧客[A]+" },
  ];
  const input = "  顧客[A]+\n顧客・顧客[A]+  ";
  assert.deepEqual(restoreText(input, rules), {
    text: "  顧客窓口 $&\n青葉・顧客窓口 $&  ", restored: 3, ambiguous: [], unknown: 0,
  });
});

test("uses only the chosen send and preserves unknown tokens as a whole", () => {
  const rules = [
    { original: "青葉", replacement: "" }, { original: "山吹", replacement: "" },
    { original: "山田", replacement: "pl" },
  ];
  const input = "<pl:company:1>、<pl:company:2>";
  assert.equal(restoreText(input, rules).text, input);
  assert.deepEqual(restoreText(input, rules, { 0: "<pl:company:1>" }), {
    text: "青葉、<pl:company:2>", restored: 1, ambiguous: [], unknown: 1,
  });
  assert.equal(restoreText(input, rules, { 1: "<pl:company:1>" }).text, "山吹、<pl:company:2>");
});

test("preserves ambiguous custom aliases and collisions with an automatic token", () => {
  const rules = [
    { original: "青葉", replacement: "顧客" }, { original: "山吹", replacement: "顧客" },
    { original: "山田", replacement: "<pl:person:1>" }, { original: "佐藤", replacement: "" },
    { original: "西田", replacement: "<pl:person:1>代理" },
  ];
  const input = "顧客・顧客・<pl:person:1>・<pl:person:1>代理";
  assert.deepEqual(restoreText(input, rules, { 3: "<pl:person:1>" }), {
    text: "顧客・顧客・<pl:person:1>・西田", restored: 1, ambiguous: ["顧客", "<pl:person:1>"], unknown: 0,
  });
});
