const app = window.Telegram?.WebApp;
const $ = id => document.getElementById(id);
const notice = $('notice');
const goods = { seed: 'Семена', grain: 'Зерно', flour: 'Мука', bread: 'Хлеб' };
const kinds = { plot: 'Грядка', mill: 'Мельница', oven: 'Печь' };
const states = { locked: 'Закрыта', unbuilt: 'Не построена', building: 'Строится', idle: 'Свободно', job: 'В работе', ready: 'Готово', leased: 'В аренде', offered: 'Сдаётся' };
let busy = false;
let ready = false;
let pending;

function el(tag, text, parent, className) {
  const node = document.createElement(tag);
  if (text) node.textContent = text;
  if (className) node.className = className;
  parent.append(node);
  return node;
}
function button(label, parent, click, className) {
  const node = el('button', label, parent, className);
  node.type = 'button';
  node.addEventListener('click', click);
  return node;
}
function timeLabel(value) {
  return new Intl.DateTimeFormat('ru', { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value * 1000));
}
function requestID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return btoa(String.fromCharCode(...bytes)).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
}
async function api(path, data = {}) {
  const response = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ init: app.initData, ...data }) });
  const dataOut = await response.json();
  if (!response.ok) {
    const error = new Error(dataOut.error);
    error.status = response.status;
    throw error;
  }
  return dataOut;
}

function tradeForm(parent, label, fields, submit) {
  const form = el('form', '', parent, 'trade-form');
  const inputs = fields.map(([name, options, max]) => {
    const field = el('label', name, form);
    if (options) {
      const select = el('select', '', field);
      for (const [key, text] of options) el('option', text, select).value = key;
      return select;
    }
    const input = el('input', '', field);
    input.type = 'number'; input.min = '1'; input.max = String(max); input.value = '1'; input.required = true;
    return input;
  });
  el('button', label, form).type = 'submit';
  form.addEventListener('submit', event => {
    event.preventDefault();
    submit(...inputs.map(input => input.value));
  });
}
function showChoices(tile, parent, view) {
  const panel = document.createElement('div');
  panel.className = 'tiles';
  const label = `${kinds[tile.kind]} ${tile.x + 1}: ` + (tile.state === 'ready' ? 'можно забрать результат' : tile.job ? `готовность ${timeLabel(tile.job.end)}` : tile.lease ? `аренда до ${timeLabel(tile.lease.end)}` : tile.available_at ? `${states[tile.state]}, до ${timeLabel(tile.available_at)}` : states[tile.state]);
  el('p', label, panel);
  for (const action of tile.actions ?? []) {
    const verb = action.split(' ')[0];
    const name = { plant: 'Посадить', mill: 'Начать помол', oven: 'Испечь хлеб', accept: 'Арендовать', unoffer: 'Снять предложение', collect: 'Забрать урожай' }[verb] ?? 'Выбрать';
    const description = verb === 'accept' && tile.offer ? `${name}: ${tile.offer.price} репы за ${tile.offer.hours} ч` : name;
    button(description, panel, () => confirm({ command: action }, description));
  }
  if (tile.owner_id === view.viewer_id && tile.state === 'idle' && tile.kind !== 'plot' && view.may_rent_out) {
    tradeForm(panel, 'Предложить аренду', [['Срок', [['6', '6 ч'], ['12', '12 ч'], ['24', '24 ч']]], ['Цена, репа', null, 100000]], (hours, price) => {
      confirm({ command: `offer ${tile.kind} ${tile.x + 1} ${hours} ${price}` }, `Сдать слот на ${hours} ч за ${price} репы?`);
    });
  }
  parent.replaceChildren(panel);
}
function render(view) {
  $('wallet').textContent = `Доступно: ${view.wallet.available} · В резерве: ${view.wallet.reserved} · Счёт звания: ${view.wallet.score}`;
  const yards = $('yards');
  const market = $('market');
  const buttons = $('buttons');
  yards.replaceChildren(); market.replaceChildren(); buttons.replaceChildren();
  if (!view.yards?.length) el('p', 'Пока никто не занял грядку.', yards);
  const map = el('div', '', yards, 'map');
  for (const yard of view.yards ?? []) {
    const node = el('article', '', map, 'yard');
    node.style.gridColumn = String(yard.yard_x + 1);
    node.style.gridRow = String(yard.yard_y + 1);
    el('h3', yard.owner_id === view.viewer_id ? 'Ваш двор' : (yard.owner_name || `Двор ${yard.owner_id}`), node);
    const stock = Object.entries(yard.inventory ?? {}).filter(([,n]) => n > 0).map(([good,n]) => `${goods[good] ?? good}: ${n}`);
    if (stock.length) el('p', stock.join(' · '), node);
    const tiles = el('div', '', node, 'yard-map');
    const choices = el('div', '', node);
    const byRow = [...(yard.tiles ?? [])].sort((a,b) => a.y - b.y || a.x - b.x);
    for (const tile of byRow) {
      const btn = button(`${kinds[tile.kind]} ${tile.x + 1}`, tiles, () => showChoices(tile, choices, view), 'tile');
      btn.style.gridColumn = String(tile.x + 1);
      btn.style.gridRow = String(tile.y + 1);
      el('small', states[tile.state], btn);
    }
  }
  if (!view.market.listings?.length) el('p', 'Пока нет товаров на продажу.', market);
  for (const item of view.market.listings ?? []) {
    const row = el('div', '', market, 'listing');
    el('span', `${goods[item.good]}: ${item.qty} шт. по ${item.unit_price} репы. До ${timeLabel(item.expires_at)}.`, row);
    if (item.seller_id === view.viewer_id) {
      button('Снять с продажи', row, () => confirm({ verb: 'cancel', arg: item.id }, 'Снять объявление?'));
    } else {
      const quantity = el('input', '', row);
      quantity.type = 'number';
      quantity.min = '1';
      quantity.max = String(item.qty);
      quantity.value = '1';
      quantity.setAttribute('aria-label', `Количество: ${goods[item.good]}`);
      button('Купить', row, () => {
        if (!quantity.reportValidity()) return;
        const n = quantity.valueAsNumber;
        if (!Number.isInteger(n) || n < 1 || n > item.qty) return;
        confirm({ verb: 'buy', arg: `${item.id}:${n}:${item.version}` }, `${goods[item.good]}, ${n} шт. за ${n * item.unit_price} репы`);
      });
    }
  }
  for (const sale of (view.market.recent ?? []).slice(0,5)) el('p', `Продано: ${goods[sale.good]}, ${sale.qty} шт. по ${sale.unit_price} репы.`, market);
  const c = view.coop;
  el('p', `Бюджет кооператива сегодня: ${c.budget_remaining} из ${c.budget_total}.`, market);
  el('p', `Общая пекарня: ${view.milestones.bread_delivered} из ${view.milestones.bread_needed} поставок хлеба.`, market);
  if (view.recovery) el('p', view.recovery, buttons);
  const prices = view.prices;
  const actions = view.joined ? [
    [`Купить семя: ${prices.seed} репы`, 'seed 1'],
    [`Купить грядку: ${prices.plot} репы`, 'plot'],
    [`Построить мельницу: ${prices.mill} репы и 2 зерна`, 'build mill'],
    [`Построить печь: ${prices.oven} репы и 1 мука`, 'build oven'],
    ['Забрать результат', 'collect'],
    ['Сдать 3 зерна за 1 репу', 'deliver grain'],
    ['Сдать 2 хлеба за 4 репы', 'deliver bread'],
  ] : [['Занять грядку', 'join']];
  for (const [label, cmd] of actions) {
    button(label, buttons, () => confirm({ command: cmd }, label));
  }
  if (view.joined) {
    tradeForm(buttons, 'Выставить на продажу', [['Товар', Object.entries(goods)], ['Количество', null, 1000], ['Цена за единицу, репа', null, 100000]], (good, qty, price) => {
      confirm({ command: `sell ${good} ${qty} ${price}` }, `${goods[good]}, ${qty} шт. по ${price} репы за единицу.`);
    });
  }
}
async function refresh(keepNotice = false) {
  try { render(await api('board')); ready = true; if (!keepNotice) notice.textContent = ''; }
  catch (e) { if (e.status === 401 || e.status === 403) ready = false; notice.textContent = e.message || 'Не удалось открыть ферму.'; }
}
function confirm(input, label) {
  if (!ready || busy) return;
  const panel = $('confirm');
  panel.replaceChildren();
  pending = { ...input, id: requestID() };
  el('p', label, panel);
  panel.scrollIntoView({ block: 'nearest' });
  const yes = button('Подтвердить', panel, async () => {
    if (busy) return;
    busy = true;
    yes.disabled = true;
    try {
      const result = await api('action', pending);
      notice.textContent = result.Text;
      panel.replaceChildren();
      pending = undefined;
      for (const b of result.Buttons ?? []) button(b.Label, panel, () => confirm({ verb: b.Verb, arg: b.Arg }, b.Label));
      await refresh(true);
    } catch (error) {
      notice.textContent = error.message || 'Ответ потерялся. Повторите подтверждение.';
      if (error.status && error.status < 500) { panel.replaceChildren(); pending = undefined; await refresh(true); }
    } finally { busy = false; yes.disabled = false; }
  });
  button('Отмена', panel, () => { if (!busy) { pending = undefined; panel.replaceChildren(); } });
}
$('refresh').addEventListener('click', () => { if (app?.initData && !busy) void refresh(); });
if (app?.initData) {
  app.ready();
  void refresh();
  setInterval(() => { if (!document.hidden && !busy && !pending && ready) void refresh(true); }, 30000);
} else {
  notice.textContent = 'Откройте ферму по кнопке в группе Telegram.';
  $('refresh').disabled = true;
}
