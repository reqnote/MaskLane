"use strict";

const get = (id) => document.getElementById(id);
const labels = { company: "企業名", project: "案件名" };
const formatter = new Intl.DateTimeFormat("ja-JP", {
  month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit",
});
const selected = new Set();
const fragment = location.hash.slice(1);
if (/^[a-f0-9]{64}$/.test(fragment)) {
  sessionStorage.setItem("masking-gateway-session", fragment);
  history.replaceState(null, "", location.pathname);
}
const session = sessionStorage.getItem("masking-gateway-session");
let snapshot = { revision: 0, rules: [] };
let editingIndex = -1;
let removingIndex = -1;
let importPlan = null;
let feedbackTimer = null;

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

function stateText(event) {
  if (event.state === "sent") {
    const counts = [];
    if (event.company) counts.push("企業名 " + event.company + " 種類");
    if (event.project) counts.push("案件名 " + event.project + " 種類");
    return counts.length ? counts.join("・") + "を置換して送信しました" : "登録名の一致なし・送信済み";
  }
  if (event.state === "unconfirmed") return "送信先から応答を確認できませんでした";
  if (event.reason === "no_mapping") return "マッピングが未登録のため、送信を停止しました";
  if (event.reason === "policy") return "検査で送信を停止しました";
  return "内容を確認できなかったため、送信を停止しました";
}

function setReady(ready) {
  const pill = get("process-state");
  pill.dataset.ready = String(ready);
  pill.lastElementChild.textContent = ready ? "起動中" : "接続なし";
}

function dateText(iso) {
  return formatter.format(new Date(iso));
}

async function refreshStatus() {
  try {
    const state = await api("state");
    setReady(true);
    get("proxy-address").textContent = state.proxy;
    get("copy-address").disabled = false;
    const [latest] = state.activity;
    get("recent-status").textContent = latest ? stateText(latest) : "Kiro の対象通信をまだ確認していません";
    if (latest) {
      get("recent-detail").textContent = dateText(latest.at);
    } else {
      get("recent-detail").textContent = state.mappingCount ? "Kiro のプロキシ設定を確認してください。" : "最初に伏せたい名前を登録してください。";
    }
    get("history").hidden = state.activity.length < 2;
    get("activity").replaceChildren(...state.activity.map((event) => {
      const item = document.createElement("li");
      const time = document.createElement("time");
      time.dateTime = event.at;
      time.textContent = dateText(event.at);
      item.append(time, document.createTextNode(stateText(event)));
      return item;
    }));
  } catch (error) {
    setReady(false);
    get("recent-status").textContent = "ゲートウェイの状態を確認できません";
    get("recent-detail").textContent = "アプリを起動し直して、新しく開いた画面をお使いください。";
    get("history").hidden = true;
    get("copy-address").disabled = true;
  }
}

function showSelection() {
  const total = snapshot.rules.length;
  get("selection").textContent = selected.size + " 件選択";
  get("all").checked = total > 0 && selected.size === total;
  get("all").indeterminate = selected.size > 0 && selected.size < total;
  get("all").disabled = total === 0;
  get("export").disabled = selected.size === 0;
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
    row.insertCell().textContent = labels[rule.kind];
    row.insertCell().textContent = rule.original;
    const replacement = row.insertCell();
    replacement.textContent = rule.replacement || "自動生成";
    if (!rule.replacement) replacement.classList.add("muted");
    const actions = row.insertCell();
    actions.append(rowButton("編集", rule, () => editMapping(index)), rowButton("削除", rule, () => removeMapping(index)));
    return row;
  });
  get("mappings").replaceChildren(...rows);
  get("empty").hidden = rows.length > 0;
  showSelection();
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
  editingIndex = index;
  const rule = snapshot.rules[index] || { kind: "company", original: "", replacement: "" };
  get("edit-title").textContent = index < 0 ? "名前を登録" : "マッピングを編集";
  get("kind").value = rule.kind;
  get("original").value = rule.original;
  get("replacement").value = rule.replacement;
  showMessage(get("edit-error"), "");
  get("edit-dialog").showModal();
  get("original").focus();
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

bindDialog("edit", async () => {
  const rule = {
    kind: get("kind").value,
    original: get("original").value.trim(),
    replacement: get("replacement").value.trim(),
  };
  const rules = snapshot.rules.slice();
  if (editingIndex < 0) rules.push(rule);
  else rules[editingIndex] = rule;
  await saveMappings(rules);
  notify("マッピングを保存しました。");
});
bindDialog("remove", async () => {
  await saveMappings(snapshot.rules.filter((_, index) => index !== removingIndex));
  notify("マッピングを削除しました。");
});
bindDialog("import", async () => {
  if (!importPlan?.added.length) return;
  await saveMappings([...snapshot.rules, ...importPlan.added], importPlan.revision);
  notify(importPlan.added.length + " 件を追加しました。");
});

get("add").addEventListener("click", () => editMapping(-1));
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
      item.textContent = labels[rule.kind] + "　" + rule.original + " → " + (rule.replacement || "自動生成");
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
  notify(rules.length + " 件をエクスポートしました。");
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
    setReady(false);
    showMessage(get("banner"), "アプリの起動時に開くダッシュボードを使ってください。", true);
    return;
  }
  try {
    snapshot = await api("mappings");
    renderMappings();
    get("add").disabled = false;
    get("import").disabled = false;
    await refreshStatus();
    setInterval(() => { void refreshStatus(); }, 2000);
  } catch (error) {
    setReady(false);
    showMessage(get("banner"), error.message, true);
  }
}

void init();
