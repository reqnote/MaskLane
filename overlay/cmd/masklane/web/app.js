import { maskText, restoreText } from "./restore.mjs";

const get = (id) => document.getElementById(id);
const labels = Array.from(get("kind").options, ({ value, textContent }) => [value, textContent]);
const kindLabel = new Map(labels);
const formatter = new Intl.DateTimeFormat("ja-JP", {
  month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit",
});
const selected = new Set();
const sessionKey = "masklane-session";
const fragment = location.hash.slice(1);
if (/^[a-f0-9]{64}$/.test(fragment)) {
  sessionStorage.setItem(sessionKey, fragment);
  history.replaceState(null, "", location.pathname + location.search);
}
const session = sessionStorage.getItem(sessionKey);
let snapshot = { revision: 0, rules: [] };
let draft = null;
let removingIndex = -1;
let importPlan = null;
let feedbackTimer = null;
let activity = [];
let comparedSendId = null;
let restoreRevision = 0;
let localAutomatic = null;
let localNumbers = {};

const menuLinks = document.querySelectorAll(".main-menu a");

function showMenuPage() {
  const view = new URLSearchParams(location.search).get("view");
  const pageId = view === "management" || view === "usage" ? view : "text-tools";
  let activeLink;
  let moveFocus = false;
  for (const link of menuLinks) {
    const page = get(link.getAttribute("aria-controls"));
    const active = page.id === pageId;
    if (!active && page.contains(document.activeElement)) moveFocus = true;
    page.hidden = !active;
    if (active) {
      link.setAttribute("aria-current", "page");
      activeLink = link;
    } else {
      link.removeAttribute("aria-current");
    }
  }
  if (moveFocus) activeLink.focus();
  if (view === "usage" && session && !get("kiro-launch-output").value) {
    void loadKiroLaunchCommand();
  }
}

for (const link of menuLinks) {
  link.addEventListener("click", (event) => {
    if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    if (!link.hasAttribute("aria-current")) {
      history.pushState(null, "", link.href);
      showMenuPage();
    }
  });
}
window.addEventListener("popstate", showMenuPage);
showMenuPage();

async function api(route, options = {}) {
  const response = await fetch("/api/" + route, {
    ...options,
    headers: {
      "X-Masking-Session": session || "",
      ...(options.body ? { "Content-Type": "application/json" } : {}),
    },
    cache: "no-store",
  });
  const payload = await response.json();
  if (!response.ok) {
    throw new Error(payload.error || "通信に失敗しました。");
  }
  return payload;
}

function showMessage(element, message, error = false) {
  element.hidden = !message;
  element.textContent = message;
  element.classList.toggle("error", error);
}

function notify(message, error = false) {
  clearTimeout(feedbackTimer);
  showMessage(get("feedback"), message, error);
  if (!error) {
    feedbackTimer = setTimeout(() => showMessage(get("feedback"), ""), 5000);
  }
}

function matchedKeywords(event) {
  return labels.filter(([kind]) => event[kind] > 0)
    .map(([kind, label]) => label + " " + event[kind] + " 種類").join("・");
}

function stateText(event, matches) {
  if (event.state === "sent") {
    return matches ? "マスキングして送信しました" : "キーワードの一致なし・送信済み";
  }
  if (event.state === "unconfirmed") return "送信先から応答を確認できませんでした";
  if (event.reason === "no_mapping") return "キーワードが未登録のため、送信を停止しました";
  if (event.reason === "body_too_large") return "送信本文が上限を超えたため、送信を停止しました";
  if (event.reason === "policy") return "検査で送信を停止しました";
  return "内容を確認できなかったため、送信を停止しました";
}

function showConnection(state, message, detail = "") {
  get("connection").dataset.state = state;
  const status = get("recent-status");
  if (status.textContent !== message) status.textContent = message;
  const recentDetail = get("recent-detail");
  recentDetail.hidden = !detail;
  recentDetail.textContent = detail;
}

function dateText(iso) {
  return formatter.format(new Date(iso));
}

function automaticToken(kind, number) {
  return "<pl:" + kind + ":" + number + ">";
}

function canCompare(event) {
  return event.mappingRevision === snapshot.revision && Boolean(event.automatic);
}

const textTabs = Array.from(get("text-tool-tabs").querySelectorAll('[role="tab"]'));

function selectTextTab(selectedTab) {
  for (const tab of textTabs) {
    const active = tab === selectedTab;
    tab.tabIndex = active ? 0 : -1;
    tab.setAttribute("aria-selected", String(active));
    get(tab.getAttribute("aria-controls")).hidden = !active;
  }
}

for (const [index, tab] of textTabs.entries()) {
  tab.addEventListener("click", () => selectTextTab(tab));
  tab.addEventListener("keydown", (event) => {
    let next;
    switch (event.key) {
      case "ArrowLeft": next = (index + textTabs.length - 1) % textTabs.length; break;
      case "ArrowRight": next = (index + 1) % textTabs.length; break;
      case "Home": next = 0; break;
      case "End": next = textTabs.length - 1; break;
      default: return;
    }
    event.preventDefault();
    selectTextTab(textTabs[next]);
    textTabs[next].focus();
  });
}

function clearTextResult(tool) {
  get(tool + "-output").value = "";
  get(tool + "-result").hidden = true;
  get(tool + "-status").textContent = "";
  get(tool + "-run").disabled = !snapshot.revision || !get(tool + "-input").value;
}

function updateRestoreSources() {
  const source = get("restore-source");
  const selectedId = source.value;
  const sends = activity.filter(canCompare);
  if (restoreRevision !== snapshot.revision) {
    localAutomatic = null;
    clearTextResult("mask");
    clearTextResult("restore");
    restoreRevision = snapshot.revision;
  }
  source.length = 1;
  if (localAutomatic) source.add(new Option("直前の簡易マスク", "local"));
  for (const send of sends) {
    source.add(new Option(dateText(send.at) + "（#" + send.id + "）", String(send.id)));
  }
  source.value = selectedId;
  if (source.selectedIndex < 0) source.selectedIndex = 0;
  get("restore-source-field").hidden = source.length === 1;
  if (source.value !== selectedId) clearTextResult("restore");
}

get("mask-input").addEventListener("input", () => clearTextResult("mask"));
get("mask-form").addEventListener("submit", (event) => {
  event.preventDefault();
  clearTextResult("mask");
  if (!snapshot.rules.length) {
    get("mask-status").textContent = "マスキングルールにキーワードを追加してください。";
    return;
  }
  const result = maskText(get("mask-input").value, snapshot.rules, localNumbers);
  if (!result.masked) {
    get("mask-status").textContent = "登録済みのキーワードは見つかりませんでした。";
    return;
  }
  localNumbers = result.numbers;
  localAutomatic = Object.keys(result.automatic).length ? result.automatic : null;
  if (get("restore-source").value === "local") clearTextResult("restore");
  updateRestoreSources();
  get("mask-output").value = result.text;
  get("mask-result").hidden = false;
  get("mask-copy").textContent = "結果をコピー";
  get("mask-status").textContent = result.masked + " か所をマスクしました。";
});

get("mask-transfer").addEventListener("click", () => {
  const input = get("restore-input");
  selectTextTab(get("restore-tab"));
  input.value = get("mask-output").value;
  get("restore-source").value = localAutomatic ? "local" : "";
  clearTextResult("restore");
  input.focus();
});

get("restore-input").addEventListener("input", () => clearTextResult("restore"));
get("restore-source").addEventListener("change", () => clearTextResult("restore"));
get("restore-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const source = get("restore-source").value;
  const automatic = source === "local" ? localAutomatic
    : activity.find((item) => String(item.id) === source && canCompare(item))?.automatic;
  const result = restoreText(get("restore-input").value, snapshot.rules, automatic);
  get("restore-output").value = result.text;
  get("restore-result").hidden = false;
  get("restore-copy").textContent = "結果をコピー";
  const messages = [result.restored ? result.restored + " か所を復元しました。" : "復元できる表記はありませんでした。"];
  if (result.unknown) {
    messages.push("対応を特定できない自動表記 " + result.unknown + " か所を残しました。");
    if (!source) messages.push(get("restore-source-field").hidden
      ? "現在のルールで照合できる履歴がありません。" : "対応する照合先を選んでください。");
  }
  if (result.ambiguous.length) messages.push("複数の元の語に対応する表記を残しました：" + result.ambiguous.join("、"));
  get("restore-status").textContent = messages.join(" ");
});

async function copyTextResult(tool) {
  const output = get(tool + "-output");
  try {
    await navigator.clipboard.writeText(output.value);
    get(tool + "-copy").textContent = "コピーしました";
  } catch {
    output.focus();
    output.select();
    get(tool + "-status").textContent += " 選択した内容を手動でコピーしてください。";
  }
}

get("mask-copy").addEventListener("click", () => copyTextResult("mask"));
get("restore-copy").addEventListener("click", () => copyTextResult("restore"));
get("kiro-launch-copy").addEventListener("click", () => copyTextResult("kiro-launch"));

async function loadKiroLaunchCommand() {
  const status = get("kiro-launch-status");
  status.textContent = "コマンドを取得中";
  try {
    const { command } = await api("kiro-launch");
    get("kiro-launch-output").value = command;
    get("kiro-launch-copy").disabled = false;
    status.textContent = "";
  } catch (error) {
    status.textContent = error.message;
  }
}

function updateComparison() {
  const sends = activity.filter(canCompare);
  const previous = sends.find((event) => event.id === comparedSendId);
  const current = previous || sends[0];
  comparedSendId = previous?.id ?? null;

  const caption = get("sent-comparison");
  const label = current ? dateText(current.at) + " の送信" : "";
  if (caption.textContent !== label) caption.textContent = label;
  caption.hidden = !current;
  get("latest-comparison").hidden = !previous || current === sends[0];

  Array.from(get("mappings").rows).forEach((row, index) => {
    const code = row.querySelector(".automatic-token");
    if (!code) return;
    const actual = current?.automatic[index];
    code.textContent = actual || automaticToken(snapshot.rules[index].kind, "…");
    code.classList.toggle("pending", !actual);
    code.title = actual ? "" : (current ? "この送信では置換されていません" : "番号は送信時に確定します");
  });
  for (const button of get("activity").querySelectorAll("[data-send-id]")) {
    button.setAttribute("aria-pressed", String(Number(button.dataset.sendId) === current?.id));
  }
}

function renderActivity() {
  get("history").hidden = activity.length < 2;
  get("activity").replaceChildren(...activity.map((event) => {
    const item = document.createElement("li");
    const time = document.createElement("time");
    time.dateTime = event.at;
    time.textContent = dateText(event.at);
    const comparable = canCompare(event);
    const result = document.createElement(comparable ? "button" : "span");
    const matches = matchedKeywords(event);
    result.textContent = [stateText(event, matches), matches].filter(Boolean).join(" · ");
    if (comparable) {
      result.type = "button";
      result.className = "quiet compact";
      result.dataset.sendId = event.id;
      result.setAttribute("aria-label", time.textContent + " の置換後の表記を表示");
      result.addEventListener("click", () => {
        comparedSendId = event.id;
        updateComparison();
      });
    }
    item.append(time, result);
    return item;
  }));
  updateComparison();
  updateRestoreSources();
}

async function refreshStatus() {
  try {
    const state = await api("state");
    get("proxy-address").textContent = state.proxy;
    get("copy-address").disabled = false;
    if ((state.activity[0]?.id ?? 0) > (activity[0]?.id ?? 0)) {
      activity = state.activity;
      renderActivity();
    }
    get("history").hidden = activity.length < 2;
    const [latest] = activity;
    if (latest) {
      const matches = matchedKeywords(latest);
      const detail = [dateText(latest.at), matches].filter(Boolean).join(" · ");
      showConnection(latest.state, stateText(latest, matches), detail);
    } else {
      showConnection("waiting", "Kiro の対象通信を待機中");
    }
  } catch (error) {
    showConnection("disconnected", "MaskLane に接続できません", "アプリを起動し直してください。");
    get("history").hidden = true;
    get("copy-address").disabled = true;
  }
}

function showSelection() {
  const total = snapshot.rules.length;
  get("selection").hidden = selected.size === 0;
  get("selection").textContent = selected.size + " 件選択";
  get("all").checked = total > 0 && selected.size === total;
  get("all").indeterminate = selected.size > 0 && selected.size < total;
  get("all").disabled = total === 0;
  get("export").disabled = selected.size === 0;
  get("export-detail").hidden = selected.size === 0;
}

function updateRuleActions() {
  get("add").hidden = Boolean(draft);
  get("add").disabled = snapshot.revision === 0;
  get("import").disabled = snapshot.revision === 0 || Boolean(draft);
  Array.from(get("mappings").rows).forEach((row, index) => {
    row.classList.toggle("editing", draft?.index === index);
    for (const action of row.querySelectorAll("button")) action.disabled = Boolean(draft);
  });
}

function renderMappings() {
  const rows = snapshot.rules.map((rule, index) => {
    const row = document.createElement("tr");
    const choice = row.insertCell();
    const check = document.createElement("input");
    check.type = "checkbox";
    check.checked = selected.has(rule.original);
    check.setAttribute("aria-label", rule.original + " を選択");
    check.addEventListener("change", () => {
      if (check.checked) selected.add(rule.original);
      else selected.delete(rule.original);
      showSelection();
    });
    choice.append(check);
    row.insertCell().textContent = kindLabel.get(rule.kind);
    const keyword = row.insertCell();
    keyword.className = "keyword";
    keyword.textContent = rule.original;
    const replacement = row.insertCell();
    if (rule.replacement) {
      replacement.textContent = rule.replacement;
    } else {
      const code = document.createElement("code");
      code.className = "automatic-token";
      replacement.append(code);
    }
    const actions = row.insertCell();
    actions.className = "row-actions";
    actions.append(rowButton("編集", rule, () => editMapping(index)), rowButton("削除", rule, () => removeMapping(index)));
    return row;
  });
  get("mappings").replaceChildren(...rows);
  get("empty").hidden = rows.length > 0;
  showSelection();
  updateRuleActions();
  renderActivity();
}

function rowButton(label, rule, click) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "quiet";
  button.textContent = label;
  button.setAttribute("aria-label", rule.original + " を" + label);
  button.addEventListener("click", click);
  return button;
}

function editMapping(index) {
  draft = { index, snapshot };
  const rule = snapshot.rules[index] || { kind: "company", original: "", replacement: "" };
  get("edit-title").textContent = index < 0 ? "キーワードを追加" : "キーワードを編集";
  get("edit-save").textContent = index < 0 ? "キーワードを登録" : "変更を保存";
  get("kind").value = rule.kind;
  get("original").value = rule.original;
  get("replacement").value = rule.replacement;
  showMessage(get("edit-error"), "");
  updateReplacementPreview();
  get("editor").hidden = false;
  updateRuleActions();
  get("original").focus();
}

function updateReplacementPreview() {
  const custom = get("replacement").value.trim();
  get("preview-label").textContent = custom ? "置換プレビュー" : "空欄時の置換例";
  get("preview-original").textContent = get("original").value.trim() || "入力したキーワード";
  get("preview-result").textContent = custom || automaticToken(get("kind").value, 1);
  get("preview-hint").hidden = Boolean(custom);
}

function closeEditor() {
  const index = draft.index;
  draft = null;
  get("editor").hidden = true;
  updateRuleActions();
  const returnTo = get("mappings").rows[index]?.querySelector("button") || get("add");
  returnTo.focus();
}

function removeMapping(index) {
  removingIndex = index;
  get("remove-description").textContent = "「" + snapshot.rules[index].original + "」を削除します。";
  showMessage(get("remove-error"), "");
  get("remove-dialog").showModal();
}

async function saveMappings(rules, revision = snapshot.revision) {
  snapshot = await api("mappings", { method: "PUT", body: JSON.stringify({ revision, rules }) });
  selected.clear();
  renderMappings();
  void refreshStatus();
}

function bindDialog(name, onSubmit) {
  const dialog = get(name + "-dialog");
  const cancel = get(name + "-cancel");
  const submit = get(name + "-save");
  cancel.addEventListener("click", () => dialog.close());
  dialog.addEventListener("cancel", (event) => {
    if (cancel.disabled) event.preventDefault();
  });
  get(name + "-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    cancel.disabled = true;
    try {
      await onSubmit();
      dialog.close();
    } catch (error) {
      showMessage(get(name + "-error"), error.message, true);
    } finally {
      submit.disabled = name === "import" && !importPlan?.added.length;
      cancel.disabled = false;
    }
  });
}

const editForm = get("edit-form");
const editCancel = get("edit-cancel");
const editSave = get("edit-save");
editForm.addEventListener("input", () => {
  showMessage(get("edit-error"), "");
  updateReplacementPreview();
});
editCancel.addEventListener("click", closeEditor);
editForm.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !editCancel.disabled) {
    event.preventDefault();
    closeEditor();
  }
});
editForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!draft || editSave.disabled) return;
  const { index, snapshot: before } = draft;
  const rule = {
    kind: get("kind").value,
    original: get("original").value.trim(),
    replacement: get("replacement").value.trim(),
  };
  const rules = before.rules.slice();
  if (index < 0) rules.push(rule);
  else rules[index] = rule;
  for (const control of editForm.elements) control.disabled = true;
  try {
    await saveMappings(rules, before.revision);
    closeEditor();
    notify(index < 0 ? "キーワードを登録しました。" : "変更を保存しました。");
  } catch (error) {
    showMessage(get("edit-error"), error.message, true);
  } finally {
    for (const control of editForm.elements) control.disabled = false;
  }
});
bindDialog("remove", async () => {
  await saveMappings(snapshot.rules.filter((_, index) => index !== removingIndex));
  notify("キーワードを削除しました。");
});
bindDialog("import", async () => {
  if (!importPlan?.added.length) return;
  await saveMappings([...snapshot.rules, ...importPlan.added], importPlan.revision);
  notify(importPlan.added.length + " 件を追加しました。");
});

get("add").addEventListener("click", () => editMapping(-1));
get("latest-comparison").addEventListener("click", () => {
  comparedSendId = null;
  updateComparison();
  get("history").querySelector("summary").focus();
});
get("all").addEventListener("change", (event) => {
  selected.clear();
  if (event.currentTarget.checked) snapshot.rules.forEach((rule) => selected.add(rule.original));
  renderMappings();
  get("all").focus();
});
get("import").addEventListener("click", () => get("import-file").click());
get("import-file").addEventListener("change", async (event) => {
  const [file] = event.currentTarget.files;
  event.currentTarget.value = "";
  if (!file) return;
  try {
    if (file.size > 2 * 1024 * 1024) throw new Error("ファイルは 2 MB 以下にしてください。");
    const text = new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer());
    importPlan = await api("import-preview", { method: "POST", body: text });
    if (importPlan.revision !== snapshot.revision) {
      snapshot = await api("mappings");
      selected.clear();
      renderMappings();
      importPlan = await api("import-preview", { method: "POST", body: text });
      if (importPlan.revision !== snapshot.revision) throw new Error("設定が更新されました。もう一度読み込んでください。");
    }
    get("import-summary").textContent = "追加 " + importPlan.added.length + " 件、既存の設定を優先 " + importPlan.duplicate + " 件";
    get("import-save").disabled = !importPlan.added.length;
    get("import-plan").replaceChildren(...importPlan.added.map((rule) => {
      const item = document.createElement("li");
      item.textContent = kindLabel.get(rule.kind) + "　" + rule.original + " → " + (rule.replacement || automaticToken(rule.kind, "…"));
      return item;
    }));
    showMessage(get("import-error"), "");
    get("import-dialog").showModal();
  } catch (error) {
    notify(error.message, true);
  }
});
get("export").addEventListener("click", () => {
  const rules = snapshot.rules.filter((rule) => selected.has(rule.original));
  if (!rules.length) return;
  const link = document.createElement("a");
  const blob = new Blob([JSON.stringify({ version: 1, rules }, null, 2)], { type: "application/json" });
  link.href = URL.createObjectURL(blob);
  link.download = "masking-mappings.json";
  link.click();
  setTimeout(() => URL.revokeObjectURL(link.href), 1000);
  notify(rules.length + " 件を書き出しました。");
});
get("copy-address").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText(get("proxy-address").textContent);
    notify("プロキシのアドレスをコピーしました。");
  } catch {
    notify("コピーできませんでした。表示されているアドレスを選択してコピーしてください。", true);
  }
});

async function init() {
  if (!session) {
    showConnection("disconnected", "アプリの起動時に開く画面を使ってください。");
    showMessage(get("banner"), "アプリの起動時に開く画面を使ってください。", true);
    return;
  }
  try {
    snapshot = await api("mappings");
    renderMappings();
    await refreshStatus();
    setInterval(() => { void refreshStatus(); }, 2000);
  } catch (error) {
    showConnection("disconnected", "MaskLane に接続できません");
    showMessage(get("banner"), error.message, true);
  }
}

void init();
