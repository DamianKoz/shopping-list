'use strict';

const $ = (id) => document.getElementById(id);
const listButton = $('list-button');
const listName = $('list-name');
const listsPanel = $('lists-panel');
const listsEl = $('lists');
const newListForm = $('new-list-form');
const newListInput = $('new-list');
const newListButton = $('new-list-button');
const listView = $('list-view');
const form = $('add-form');
const input = $('new-item');
const addButton = $('add-button');
const openList = $('open-list');
const doneList = $('done-list');
const doneSection = $('done-section');
const doneSummary = $('done-summary');
const clearDone = $('clear-done');
const empty = $('empty');
const status = $('status');
const errorBox = $('error');
const menu = $('item-menu');
const menuTitle = $('menu-title');
const menuActions = $('menu-actions');
const menuLists = $('menu-lists');
const menuTop = $('menu-top');
const menuMove = $('menu-move');
const menuDelete = $('menu-delete');

const OFFLINE_MSG = 'Keine Verbindung – nicht gespeichert.';

let lists = [];
let currentId = Number(stored('list')) || null;
// Incremented on every load and local change. A load only renders if nothing
// newer happened in the meantime, so stale responses never win.
let seq = 0;
let offline = false;
let offlineTimer = null;
let reconnectTimer = null;
let errorTimer = null;
let events = null;
let busy = false;

function stored(key) {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function store(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {}
}

// The selected list is remembered per device; fall back to the first one if
// it was deleted.
function current() {
  return lists.find((l) => l.id === currentId) || lists[0] || null;
}

// --- Rendering ---

function render() {
  const focusKey = document.activeElement && document.activeElement.dataset.key;
  const list = current();
  const items = list ? list.items : [];

  listName.textContent = list ? list.name : 'Einkauf';
  document.title = listName.textContent;
  listsEl.replaceChildren(...lists.map(listRow));

  const sorted = [...items].sort((a, b) => a.position - b.position || a.id - b.id);
  const done = sorted.filter((i) => i.completed);
  openList.replaceChildren(...sorted.filter((i) => !i.completed).map(itemRow));
  doneList.replaceChildren(...done.map(itemRow));
  doneSection.hidden = done.length === 0;
  doneSummary.textContent = `Erledigt (${done.length})`;
  empty.hidden = items.length > 0;
  for (const el of [addButton, clearDone, newListButton]) el.disabled = offline;

  // Keep keyboard focus on the same control after re-rendering.
  if (focusKey) {
    const el = document.querySelector(`[data-key="${focusKey}"]`);
    if (el) el.focus();
  }
}

function listRow(list) {
  const name = document.createElement('span');
  name.textContent = list.name;
  const count = document.createElement('span');
  count.className = 'count';
  count.textContent = list.items.filter((i) => !i.completed).length || '';

  const select = document.createElement('button');
  select.type = 'button';
  select.className = 'list-select';
  select.dataset.key = `list-${list.id}`;
  if (list === current()) select.setAttribute('aria-current', 'true');
  select.append(name, count);
  select.addEventListener('click', () => {
    currentId = list.id;
    store('list', list.id);
    closePanel();
    render();
  });

  const li = document.createElement('li');
  li.append(select);
  if (lists.length > 1) {
    li.append(deleteButton(`Liste ${list.name} löschen`, `delete-list-${list.id}`, () => removeList(list)));
  }
  return li;
}

function itemRow(item) {
  const box = document.createElement('input');
  box.type = 'checkbox';
  box.checked = item.completed;
  box.disabled = offline;
  box.dataset.key = `check-${item.id}`;
  box.addEventListener('change', () => toggle(item, box.checked));

  const name = document.createElement('span');
  name.textContent = item.name;

  const label = document.createElement('label');
  label.append(box, name);

  const li = document.createElement('li');
  li.append(label, rowButton('…', `Aktionen für ${item.name}`, `menu-${item.id}`, () => openMenu(item)));
  return li;
}

function deleteButton(label, key, onClick) {
  return rowButton('×', label, key, onClick);
}

function rowButton(text, label, key, onClick) {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'row-button';
  button.textContent = text;
  button.disabled = offline;
  button.dataset.key = key;
  button.setAttribute('aria-label', label);
  button.addEventListener('click', onClick);
  return button;
}

function showError(msg) {
  errorBox.textContent = msg;
  errorBox.hidden = false;
  clearTimeout(errorTimer);
  errorTimer = setTimeout(hideError, 6000);
}

function hideError() {
  errorBox.hidden = true;
}

// --- Lists panel ---

function openPanel() {
  listsPanel.hidden = false;
  listView.hidden = true;
  listButton.setAttribute('aria-expanded', 'true');
}

function closePanel() {
  listsPanel.hidden = true;
  listView.hidden = false;
  listButton.setAttribute('aria-expanded', 'false');
}

listButton.addEventListener('click', () => (listsPanel.hidden ? openPanel() : closePanel()));
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !listsPanel.hidden) {
    closePanel();
    listButton.focus();
  }
});

// --- Item menu (action sheet) ---

let menuItem = null;

function openMenu(item) {
  menuItem = item;
  menuTitle.textContent = item.name;
  menuTop.hidden = item.completed;
  menuMove.hidden = lists.length < 2;
  menuActions.hidden = false;
  menuLists.hidden = true;
  menu.showModal();
}

// menuAction closes the menu and then performs the chosen action.
function menuAction(action) {
  return () => {
    const item = menuItem;
    menu.close();
    action(item);
  };
}

menuTop.addEventListener('click', menuAction((item) => update(item, { to_top: true })));
menuDelete.addEventListener('click', menuAction(remove));
menuMove.addEventListener('click', () => {
  menuTitle.textContent = 'Verschieben nach';
  menuLists.replaceChildren(
    ...lists
      .filter((l) => l.id !== menuItem.list_id)
      .map((l) => {
        const button = document.createElement('button');
        button.type = 'button';
        button.textContent = l.name;
        button.addEventListener('click', menuAction((item) => update(item, { list_id: l.id })));
        return button;
      }),
  );
  menuActions.hidden = true;
  menuLists.hidden = false;
  menu.focus();
});
$('menu-cancel').addEventListener('click', () => menu.close());
// A click on the dialog itself (not its buttons) is a click on the backdrop.
menu.addEventListener('click', (e) => {
  if (e.target === menu) menu.close();
});

// --- Data ---

async function api(method, path, body) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : {},
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(8000),
    });
  } catch {
    throw new Error(OFFLINE_MSG);
  }
  if (res.status === 401) {
    location.href = '/login';
    throw new Error('Nicht angemeldet.');
  }
  if (!res.ok) {
    let msg = 'Das hat nicht geklappt.';
    try {
      msg = (await res.json()).error || msg;
    } catch {}
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

async function load() {
  const mine = ++seq;
  try {
    const data = await api('GET', '/api/lists');
    if (mine !== seq) return;
    lists = data;
    save();
    render();
  } catch {
    // Connectivity is reported by the status banner.
  }
}

// applyLocal shows a confirmed change right away and invalidates older loads.
function applyLocal(change) {
  seq++;
  lists = change(lists);
  save();
  render();
}

// placeItem returns a change that puts an updated item into its (maybe new) list.
function placeItem(updated) {
  return (all) =>
    all.map((l) => {
      const items = l.items.filter((i) => i.id !== updated.id);
      if (l.id === updated.list_id) items.push(updated);
      return { ...l, items };
    });
}

// changeItems returns a change that applies fn to the items of one list.
function changeItems(listId, fn) {
  return (all) => all.map((l) => (l.id === listId ? { ...l, items: fn(l.items) } : l));
}

function save() {
  store('lists', JSON.stringify(lists));
}

function restore() {
  try {
    const cached = JSON.parse(stored('lists'));
    if (Array.isArray(cached)) lists = cached;
  } catch {}
}

// --- Actions ---

async function mutate(action) {
  if (offline) {
    showError(OFFLINE_MSG);
    render();
    return;
  }
  try {
    await action();
    hideError();
  } catch (err) {
    showError(err.message);
    render(); // reset e.g. a checkbox the browser already toggled
  }
  load();
}

// submitName handles both "add" forms: ignores empty input and double submits,
// and only clears the field once the server confirmed.
function submitName(field, action) {
  const name = field.value.trim();
  if (!name || busy) return;
  busy = true;
  mutate(async () => {
    await action(name);
    field.value = '';
  }).finally(() => {
    busy = false;
  });
}

form.addEventListener('submit', (e) => {
  e.preventDefault();
  const list = current();
  if (!list) return;
  submitName(input, async (name) => {
    const item = await api('POST', `/api/lists/${list.id}/items`, { name });
    applyLocal(changeItems(list.id, (items) => [...items, item]));
  });
});

newListForm.addEventListener('submit', (e) => {
  e.preventDefault();
  submitName(newListInput, async (name) => {
    const created = await api('POST', '/api/lists', { name });
    currentId = created.id;
    store('list', created.id);
    applyLocal((all) => [...all, created]);
    closePanel();
  });
});

function toggle(item, completed) {
  update(item, { completed });
}

function update(item, changes) {
  mutate(async () => {
    const updated = await api('PATCH', `/api/items/${item.id}`, changes);
    applyLocal(placeItem(updated));
  });
}

function remove(item) {
  mutate(async () => {
    await api('DELETE', `/api/items/${item.id}`);
    applyLocal(changeItems(item.list_id, (items) => items.filter((i) => i.id !== item.id)));
  });
}

function removeList(list) {
  if (!confirm(`„${list.name}“ mit allen Artikeln löschen?`)) return;
  mutate(async () => {
    await api('DELETE', `/api/lists/${list.id}`);
    applyLocal((all) => all.filter((l) => l.id !== list.id));
  });
}

clearDone.addEventListener('click', () => {
  const list = current();
  if (!list) return;
  mutate(async () => {
    await api('DELETE', `/api/lists/${list.id}/items/completed`);
    applyLocal(changeItems(list.id, (items) => items.filter((i) => !i.completed)));
  });
});

// Whether completed items are shown is remembered per device.
doneSection.open = stored('showDone') !== '0';
doneSection.addEventListener('toggle', () => store('showDone', doneSection.open ? '1' : '0'));

// --- Live updates ---

function setOffline(value) {
  if (offline === value) return;
  offline = value;
  status.hidden = !value;
  render();
}

function connect() {
  clearTimeout(reconnectTimer);
  if (events) events.close();

  // Only show the banner if the connection stays down for a moment, so quick
  // reconnects (e.g. when the app returns to the foreground) don't flicker.
  clearTimeout(offlineTimer);
  offlineTimer = setTimeout(() => setOffline(true), 2000);

  events = new EventSource('/api/events');
  events.onopen = () => {
    clearTimeout(offlineTimer);
    setOffline(false);
    load();
  };
  events.onmessage = load;
  events.onerror = () => {
    clearTimeout(offlineTimer);
    offlineTimer = setTimeout(() => setOffline(true), 2000);
    // EventSource reconnects by itself unless the server rejected the stream
    // (e.g. expired session). load() redirects to the login in that case.
    if (events.readyState === EventSource.CLOSED) {
      load();
      reconnectTimer = setTimeout(connect, 5000);
    }
  };
}

// Mobile browsers silently drop connections in the background, so reconnect
// and reload whenever the app becomes visible again.
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') connect();
});
window.addEventListener('pageshow', (e) => {
  if (e.persisted) connect();
});
window.addEventListener('online', connect);
window.addEventListener('offline', () => setOffline(true));

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

restore();
render();
load();
connect();
