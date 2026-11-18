const app = window.Telegram?.WebApp;
const $ = id => document.getElementById(id);
const notice = $('notice');
const content = $('content');
const dialog = $('confirm');
const nav = $('navigation');
const goods = { seed: 'Семена', grain: 'Зерно', flour: 'Мука', bread: 'Хлеб' };
const kinds = { plot: 'Грядка', mill: 'Мельница', oven: 'Печь' };
const states = { locked: 'Не куплена', unbuilt: 'Место под стройку', building: 'Строится', idle: 'Свободно', job: 'В работе', ready: 'Готово', leased: 'В аренде', offered: 'Сдаётся' };
const timeFormat = new Intl.DateTimeFormat('ru', { dateStyle: 'short', timeStyle: 'short' });
let board, selectedYard, selectedTile, pending;
let page = 'map', marketGood = '', busy = false, refreshing = false, authorized = false;

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
function action(parent, label, command, { primary = false, confirm = false, reason = '' } = {}) {
  const node = button(label, parent, () => act(command, label, confirm), primary ? 'primary' : '');
  if (reason) {
    node.disabled = true;
    el('small', reason, parent, 'muted');
  }
  return node;
}
function requestID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return btoa(String.fromCharCode(...bytes)).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
}
function timeLabel(at) {
  return timeFormat.format(new Date(at * 1000));
}
function remaining(at) {
  const minutes = Math.max(1, Math.ceil((at - board.now) / 60));
  const hours = Math.floor(minutes / 60);
  return hours ? `${hours} ч ${minutes % 60} мин` : `${minutes} мин`;
}
function reputation(n) {
  const value = Number(n), last = value % 10, teen = value % 100;
  const unit = teen >= 11 && teen <= 14 ? 'очков' : last === 1 ? 'очко' : last >= 2 && last <= 4 ? 'очка' : 'очков';
  return `${n} ${unit} репутации`;
}
function count(good) {
  return board.me?.inventory?.[good] ?? 0;
}
function walletReason(cost) {
  return cost > board.wallet.available ? `Доступно ${reputation(board.wallet.available)}.` : '';
}
function yardName(yard) {
  if (yard.owner_id === board.viewer_id) return 'Ваш двор';
  return yard.owner_name ? `Двор: ${yard.owner_name}` : `Двор ${yard.yard_x + 1}, ряд ${yard.yard_y + 1}`;
}
async function api(path, data = {}) {
  const response = await fetch(path, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ init: app.initData, ...data }),
  });
  const dataOut = await response.json();
  if (!response.ok) {
    const error = new Error(dataOut.error);
    error.status = response.status;
    throw error;
  }
  return dataOut;
}

function numeric(parent, label, max) {
  const field = el('label', label, parent);
  const input = el('input', '', field);
  input.type = 'number';
  input.min = '1';
  input.max = String(max);
  input.value = '1';
  input.required = true;
  return input;
}
function selectFrom(parent, label, options) {
  const field = el('label', label, parent);
  const select = el('select', '', field);
  for (const [value, text] of options) el('option', text, select).value = value;
  return select;
}
function submit(form, label, handler) {
  el('button', label, form).type = 'submit';
  form.addEventListener('submit', event => {
    event.preventDefault();
    handler();
  });
}
function openMarket(good = '') {
  marketGood = good;
  navigate('market');
}
function navigate(next) {
  page = next;
  render();
  content.querySelector('h2')?.focus();
}
function buyPlot(parent) {
  const price = board.prices.plot;
  action(parent, `Купить грядку за ${reputation(price)}`, 'plot', { primary: true, confirm: true, reason: walletReason(price) });
}
function buildAsset(parent, kind) {
  const mill = kind === 'mill';
  const need = mill ? board.prices.mill_grain : board.prices.oven_flour;
  const good = mill ? 'grain' : 'flour';
  const material = mill ? 'зерна' : 'муки';
  const price = board.prices[kind];
  const goal = mill ? 'мельницу' : 'печь';
  const reason = count(good) < need ? `В амбаре ${count(good)} ед. ${material}, нужно ${need}.` : walletReason(price);
  action(parent, `Построить ${goal}: ${reputation(price)} и ${need} ед. ${material}`, `build ${kind}`, { primary: true, confirm: true, reason });
  if (count(good) < need) button(`Найти ${mill ? 'зерно' : 'муку'} на рынке`, parent, () => openMarket(good));
}
function showTile(tile, yard, panel) {
  panel.replaceChildren();
  const own = yard.owner_id === board.viewer_id;
  const label = `${kinds[tile.kind]} ${tile.x + 1}`;
  const plot = tile.kind === 'plot', mill = tile.kind === 'mill';
  el('h3', label, panel);
  el('p', states[tile.state], panel, 'tile-state');
  if (tile.job) {
    const job = tile.job;
    el('p', `К сбору: ${goods[job.good].toLowerCase()} (${job.qty}).`, panel);
    if (tile.state !== 'ready') el('p', `Готовность: ${timeLabel(job.end)}. Осталось ${remaining(job.end)}.`, panel);
  }
  if (tile.state === 'building') el('p', `Стройка закончится ${timeLabel(tile.ready_at)}. Осталось ${remaining(tile.ready_at)}.`, panel);
  if (tile.offer) {
    el('p', `Аренда на ${tile.offer.hours} ч за ${reputation(tile.offer.price)}. Предложение до ${timeLabel(tile.offer.expires_at)}.`, panel);
  }
  if (tile.lease) {
    const whose = tile.lease.renter_id === board.viewer_id ? 'Вы арендуете' : 'Оборудование арендовано';
    el('p', `${whose} до ${timeLabel(tile.lease.end)}. Осталось ${remaining(tile.lease.end)}.`, panel);
  }
  if (own && (tile.state === 'locked' || tile.state === 'unbuilt')) {
    const first = yard.tiles.find(t => t.kind === tile.kind && t.state === tile.state);
    if (first.id !== tile.id) {
      el('p', `Сначала откройте место №${first.x + 1}.`, panel);
      return;
    }
    if (plot) buyPlot(panel);
    else {
      el('p', 'Строительство занимает 1 час.', panel, 'muted');
      buildAsset(panel, tile.kind);
    }
  }
  for (const command of tile.actions ?? []) {
    const [verb] = command.split(' ');
    if (verb === 'plant') {
      const starter = board.me.starter_seed;
      el('p', 'Одно семя даёт 3 зерна за 4 часа.', panel);
      if (starter || count('seed')) action(panel, starter ? 'Посеять стартовое семя' : 'Посеять семя', command, { primary: true });
      else {
        el('p', 'Семян нет. Сравните цену в магазине и на рынке.', panel);
        button('Купить семена в магазине', panel, () => navigate('shop'), 'primary');
        button('Семена на рынке', panel, () => openMarket('seed'));
      }
    } else if (verb === 'collect') {
      action(panel, plot ? 'Собрать урожай' : 'Забрать продукцию', command, { primary: true });
    } else if (verb === 'mill' || verb === 'oven') {
      const good = mill ? 'grain' : 'flour';
      const enoughTime = !tile.lease || tile.lease.end - board.now >= 2 * 3600;
      el('p', mill ? 'Из 2 ед. зерна получится 1 ед. муки за 2 часа.' : 'Из 2 ед. муки получится 2 хлеба за 2 часа.', panel);
      const reason = !enoughTime ? 'До конца аренды осталось меньше двух часов.' : count(good) < 2 ? `В амбаре ${count(good)} ед., нужно 2.` : '';
      action(panel, mill ? 'Начать помол' : 'Испечь хлеб', command, { primary: true, reason });
      if (count(good) < 2) button('Купить сырьё на рынке', panel, () => openMarket(good));
    } else if (verb === 'accept') {
      action(panel, `Арендовать на ${tile.offer.hours} ч за ${reputation(tile.offer.price)}`, command, { primary: true, confirm: true, reason: walletReason(tile.offer.price) });
    } else if (verb === 'unoffer') {
      action(panel, 'Снять предложение аренды', command, { confirm: true });
    }
  }
  if (own && tile.state === 'idle' && !plot) {
    if (!board.may_rent_out) {
      el('p', 'Сдавать оборудование в аренду можно с титулом "Мастер".', panel, 'muted');
      return;
    }
    const details = el('details', '', panel);
    el('summary', 'Сдать в аренду', details);
    const form = el('form', '', details);
    const hours = selectFrom(form, 'Срок', [['6', '6 ч'], ['12', '12 ч'], ['24', '24 ч']]);
    const price = numeric(form, 'Цена за весь срок (репа)', 100000);
    el('p', 'Слот нельзя использовать, пока предложение активно. Через час без сделки оно истечёт.', form, 'muted');
    submit(form, 'Выставить предложение', () => act(`offer ${tile.kind} ${tile.x + 1} ${hours.value} ${price.value}`, `Сдать: ${label.toLowerCase()}, ${hours.value} ч за ${reputation(price.value)}?`, true));
  }
}

function heading(label, parent) {
  const h = el('h2', label, parent);
  h.tabIndex = -1;
  return h;
}
function renderMap() {
  const yard = board.yards.find(y => y.owner_id === selectedYard) ?? board.me ?? board.yards[0];
  if (yard) selectedYard = yard.owner_id;
  const layout = el('div', '', content, 'map-layout');
  const field = el('section', '', layout);
  heading(yard ? yardName(yard) : 'Новый двор', field);
  const terrain = el('div', '', field, 'terrain');
  const canvas = el('canvas', '', terrain);
  canvas.width = 320;
  canvas.height = 208;
  canvas.setAttribute('aria-hidden', 'true');
  paintYard(canvas, yard?.tiles ?? [], yard, board.now);
  const panel = el('section', '', layout, 'context');
  if (yard) {
    const tileButtons = el('div', '', terrain, 'tile-grid');
    const tiles = yard.tiles;
    const current = tiles.find(tile => tile.id === selectedTile) ??
      tiles.find(tile => tile.state === 'ready' && tile.actions?.length) ?? tiles[0];
    selectedTile = current?.id;
    function pick(tile) {
      selectedTile = tile.id;
      for (const node of tileButtons.children) node.setAttribute('aria-pressed', String(node.dataset.tile === tile.id));
      showTile(tile, yard, panel);
    }
    for (const tile of tiles) {
      const label = `${kinds[tile.kind]} ${tile.x + 1}`;
      const btn = button('', tileButtons, () => pick(tile), 'tile');
      btn.dataset.tile = tile.id;
      btn.dataset.state = tile.state;
      btn.style.gridColumn = tile.x + 1;
      btn.style.gridRow = tile.y + 1;
      btn.setAttribute('aria-controls', 'tile-details');
      const state = tile.kind === 'plot' && tile.state === 'job' ? 'Растёт' : states[tile.state];
      btn.setAttribute('aria-label', `${label}: ${state}`);
      btn.title = `${label}: ${state}`;
      const mark = tile.state === 'ready' ? ' ✓' : tile.state === 'offered' ? ' А' : '';
      el('span', `${tile.x + 1}${mark}`, btn).setAttribute('aria-hidden', 'true');
    }
    panel.id = 'tile-details';
    if (current) pick(current);
    const stock = el('div', '', field, 'stock');
    el('h3', yard.owner_id === board.viewer_id ? 'Ваш амбар' : 'Амбар двора', stock);
    el('p', Object.entries(goods).map(([id, label]) => `${label}: ${yard.inventory[id] ?? 0}`).join(', '), stock);
    if (yard.starter_seed) el('p', 'Стартовое семя можно только посеять.', stock, 'muted');
    const reserved = Object.entries(goods).filter(([id]) => yard.reserved[id] > 0);
    if (reserved.length) el('p', `На рынке: ${reserved.map(([id, label]) => `${label.toLowerCase()} ${yard.reserved[id]}`).join(', ')}.`, stock, 'muted');
    if (yard.owner_id === board.viewer_id && board.wallet.available === 0) {
      const guidance = yard.starter_seed ? 'Репа закончилась. Посейте стартовое семя.' :
        Object.values(yard.inventory).some(n => n > 0) ? 'Репа закончилась. Вы можете продать товар на рынке.' :
          'Репы и товара нет. Пополнить баланс можно похвалой от участников чата.';
      el('p', guidance, stock, 'muted');
    }
    if (yard.owner_id === board.viewer_id && Object.values(yard.inventory).some(n => n > 0)) {
      const links = el('div', '', stock, 'controls');
      button('Продать товар', links, () => openMarket());
      if (count('grain') >= board.coop.grain_qty || count('bread') >= board.coop.bread_qty) {
        button('Заказы кооператива', links, () => navigate('coop'));
      }
    }
  }
  if (!board.joined) {
    const join = el('div', '', panel, 'join');
    el('p', 'Займите свой двор. Вы получите грядку и одно стартовое семя.', join);
    action(join, 'Занять грядку', 'join', { primary: true });
  }
  if (yard) {
    const neighborhood = el('section', '', content, 'neighborhood');
    heading('Соседние дворы', neighborhood);
    const list = el('div', '', neighborhood, 'yard-list');
    if (board.yards.length === 1) el('p', 'Других дворов пока нет.', list, 'muted');
    else {
      const nextYards = board.me ? [board.me, ...board.yards.filter(y => y.owner_id !== board.viewer_id)] : board.yards;
      for (const other of nextYards) {
        const available = other.tiles.filter(tile => tile.offer && tile.owner_id !== board.viewer_id).length;
        const rental = other.tiles.filter(tile => tile.lease?.renter_id === board.viewer_id).length;
        const choice = button(yardName(other), list, () => {
          selectedYard = other.owner_id;
          selectedTile = other.tiles.find(tile => tile.lease?.renter_id === board.viewer_id ||
            (tile.offer && tile.owner_id !== board.viewer_id))?.id;
          navigate('map');
        });
        choice.setAttribute('aria-pressed', String(other.owner_id === yard.owner_id));
        if (available || rental) el('small', rental ? `У вас в аренде: ${rental}` : `Сдаётся: ${available}`, choice);
      }
    }
  }
}

function renderShop() {
  heading('Магазин', content);
  const available = board.wallet.available;
  const seedPrice = board.prices.seed;
  const seed = el('section', '', content, 'entry');
  el('h3', 'Семена', seed);
  el('p', `Цена за одно семя: ${reputation(seedPrice)}. В амбаре: ${count('seed')}.`, seed);
  if (available < seedPrice) el('p', `На покупку семян нужно ${reputation(seedPrice)}, доступно ${reputation(available)}.`, seed, 'muted');
  else {
    const form = el('form', '', seed);
    const quantity = numeric(form, 'Количество', Math.min(50, Math.floor(available / seedPrice)));
    submit(form, 'Купить семена', () => act(`seed ${quantity.value}`, `Купить семена: ${quantity.value} шт. за ${reputation(quantity.valueAsNumber * seedPrice)}?`, true));
  }
  const land = el('section', '', content, 'entry');
  el('h3', 'Грядка', land);
  const plots = board.me?.plots ?? 1;
  el('p', `Участков: ${plots} из 3.`, land);
  if (plots < 3) buyPlot(land);
  for (const kind of ['mill', 'oven']) {
    const entry = el('section', '', content, 'entry');
    el('h3', kinds[kind], entry);
    const complete = board.me?.tiles.filter(tile => tile.kind === kind && tile.built).length ?? 0;
    el('p', `Построено или строится: ${complete} из 2.`, entry);
    if (complete < 2) buildAsset(entry, kind);
  }
}

function renderMarket() {
  heading('Рынок', content);
  const list = el('section', '', content, 'entry');
  el('h3', 'Предложения игроков', list);
  const filter = selectFrom(list, 'Товар', [['', 'Все товары'], ...Object.entries(goods)]);
  filter.value = marketGood;
  filter.addEventListener('change', () => {
    marketGood = filter.value;
    render();
    content.querySelector('select').focus();
  });
  const listings = board.market.listings.filter(item => !marketGood || item.good === marketGood);
  if (!listings.length) el('p', 'По этому фильтру нет объявлений.', list, 'muted');
  for (const item of listings) {
    const row = el('article', '', list, 'listing');
    const self = item.seller_id === board.viewer_id;
    const seller = board.yards.find(y => y.owner_id === item.seller_id);
    el('h4', goods[item.good], row);
    el('p', `${item.qty} шт. по ${reputation(item.unit_price)}. До ${timeLabel(item.expires_at)}.`, row);
    if (seller) el('p', self ? 'Ваше объявление' : yardName(seller), row, 'muted');
    if (self) action(row, 'Снять с продажи', `cancel ${item.id}`, { confirm: true });
    else {
      const max = Math.min(item.qty, Math.floor(board.wallet.available / item.unit_price));
      if (max < 1) el('p', `Доступно ${reputation(board.wallet.available)}.`, row, 'muted');
      else {
        const form = el('form', '', row);
        const quantity = numeric(form, 'Количество', max);
        submit(form, 'Купить', () => act(`buy ${item.id}:${quantity.value}:${item.version}`, `${goods[item.good]}: ${quantity.value} шт. за ${reputation(quantity.valueAsNumber * item.unit_price)}. Купить?`, true));
      }
    }
  }
  const stock = Object.entries(goods).filter(([good]) => count(good));
  const sell = el('section', '', content, 'entry');
  el('h3', 'Продать товар', sell);
  if (!stock.length) el('p', 'В амбаре нет товара для продажи. Стартовое семя продать нельзя.', sell, 'muted');
  else {
    const form = el('form', '', sell);
    const good = selectFrom(form, 'Товар', stock.map(([id, name]) => [id, `${name} (${count(id)})`]));
    const quantity = numeric(form, 'Количество', Math.min(1000, count(good.value)));
    good.addEventListener('change', () => { quantity.max = String(Math.min(1000, count(good.value))); quantity.value = '1'; });
    const price = numeric(form, 'Цена за единицу (репа)', 100000);
    submit(form, 'Выставить на сутки', () => act(`sell ${good.value} ${quantity.value} ${price.value}`, `${goods[good.value]}: ${quantity.value} шт. по ${reputation(price.value)} за штуку. Выставить на сутки?`, true));
  }
  const recent = board.market.recent.filter(sale => !marketGood || sale.good === marketGood);
  if (recent.length) {
    el('h3', 'Последние сделки', sell);
    for (const sale of recent.slice(0, 5)) el('p', `${goods[sale.good]}: ${sale.qty} шт. по ${reputation(sale.unit_price)}. ${timeLabel(sale.at)}.`, sell);
  }
}

function renderCoop() {
  heading('Кооператив', content);
  const c = board.coop;
  el('p', `Бюджет на сегодня: ${c.budget_remaining} из ${c.budget_total}. Ваши поставки: ${c.fills_today} из ${c.max_fills}. Смена заказов в 00:00 UTC.`, content);
  for (const [good, qty, pay, capacity] of [
    ['grain', c.grain_qty, c.grain_pay, c.grain_left],
    ['bread', c.bread_qty, c.bread_pay, c.bread_left + c.bread2_left],
  ]) {
    const section = el('section', '', content, 'entry');
    el('h3', goods[good], section);
    el('p', `Партия: ${qty} шт. Оплата: ${reputation(pay)}. Осталось заказов: ${capacity}.`, section);
    const reason = !capacity || c.budget_remaining < pay ? 'На сегодня заказы закончились.' :
      c.fills_today >= c.max_fills ? 'Вы исчерпали дневной лимит.' : count(good) < qty ? `В амбаре ${count(good)} шт., нужно ${qty}.` : '';
    action(section, 'Сдать партию', `deliver ${good}`, { reason });
  }
  el('p', c.bakery_unlocked ? 'Общая пекарня открыта. Действует второй заказ на хлеб.' :
    `Общая пекарня откроется после ${c.bakery_needed} поставок хлеба. Принято: ${c.bread_delivered}.`, content);
  if (c.history?.length) {
    el('h3', 'Принятые поставки', content);
    for (const fill of c.history) el('p', `${goods[fill.good]}: ${fill.qty} шт. за ${reputation(fill.pay)}. ${timeLabel(fill.at)}.`, content);
  }
}

function render() {
  if (!board || !authorized) return;
  $('wallet').textContent = `Доступно: ${reputation(board.wallet.available)}. В резерве: ${board.wallet.reserved}. Очки звания: ${board.wallet.score}.`;
  for (const node of nav.querySelectorAll('[data-page]')) {
    if (node.dataset.page === page) node.setAttribute('aria-current', 'page');
    else node.removeAttribute('aria-current');
  }
  content.replaceChildren();
  ({ map: renderMap, shop: renderShop, market: renderMarket, coop: renderCoop })[page]();
  nav.hidden = content.hidden = false;
}
async function refresh(keepNotice = false) {
  if (refreshing) return;
  const active = document.activeElement;
  const focusedTile = active?.dataset.tile;
  refreshing = true;
  $('refresh').disabled = true;
  try {
    const joined = board?.joined;
    board = await api('/board');
    if (!joined && board.joined) {
      selectedYard = board.viewer_id;
      selectedTile = undefined;
    }
    authorized = true;
    if (!keepNotice) notice.textContent = '';
    render();
    if (focusedTile && document.activeElement === document.body) {
      content.querySelector(`[data-tile="${focusedTile}"]`)?.focus({ preventScroll: true });
    }
  } catch (error) {
    if (error.status === 401 || error.status === 403) {
      authorized = false;
      nav.hidden = content.hidden = true;
    }
    notice.textContent = error.status ? error.message : 'Связь с фермой прервалась. Нажмите "Обновить".';
  } finally {
    refreshing = false;
    $('refresh').disabled = false;
  }
}
function act(command, label, confirm) {
  if (!authorized || busy || refreshing || pending) return;
  pending = { command, id: requestID() };
  $('confirm-copy').textContent = label;
  $('confirm-error').hidden = true;
  $('yes').textContent = 'Подтвердить';
  $('no').disabled = false;
  if (confirm) dialog.showModal();
  else void send();
}
async function send() {
  if (!pending || busy) return;
  busy = true;
  $('yes').disabled = $('no').disabled = true;
  notice.textContent = 'Отправляем действие...';
  try {
    const result = await api('/action', pending);
    pending = undefined;
    dialog.close();
    notice.textContent = result.Text;
    await refresh(true);
  } catch (error) {
    if (error.status && error.status < 500) {
      pending = undefined;
      dialog.close();
      notice.textContent = error.message;
      await refresh(true);
    } else {
      notice.textContent = '';
      $('confirm-error').hidden = false;
      $('confirm-error').textContent = 'Подтверждение не пришло. Повторите тот же запрос: повторного списания не будет.';
      $('yes').textContent = 'Повторить запрос';
      if (!dialog.open) dialog.showModal();
    }
  } finally {
    busy = false;
    $('yes').disabled = false;
    $('no').disabled = Boolean(pending);
  }
}
$('yes').addEventListener('click', () => void send());
$('no').addEventListener('click', () => {
  pending = undefined;
  dialog.close();
});
dialog.addEventListener('cancel', event => {
  if ($('no').disabled) event.preventDefault();
  else pending = undefined;
});
nav.addEventListener('click', event => {
  const next = event.target.closest('[data-page]')?.dataset.page;
  if (next && !busy) navigate(next);
});
$('refresh').addEventListener('click', () => { if (app?.initData && !busy) void refresh(); });
if (app?.initData) {
  app.ready();
  void refresh();
  setInterval(() => {
    const active = document.activeElement;
    const editing = content.contains(active) && !active.dataset.tile;
    if (!document.hidden && !busy && !pending && authorized && !editing) void refresh(true);
  }, 30000);
} else {
  notice.textContent = 'Откройте ферму по кнопке в группе Telegram.';
  $('refresh').disabled = true;
}
