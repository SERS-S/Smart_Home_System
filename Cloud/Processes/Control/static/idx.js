// All displayed readings come from Control; no demo values or local automation.
export function formatValue(value, digits = 0) {
  return typeof value === 'number' && Number.isFinite(value)
    ? new Intl.NumberFormat('ru-RU', { maximumFractionDigits: digits }).format(value)
    : '—';
}

export function chartGeometry(points, width = 360, height = 72) {
  const values = (points ?? []).filter(p => typeof p.value === 'number' && Number.isFinite(p.value));
  if (!values.length) return { line: '', fill: '', last: null, count: 0 };
  const numbers = values.map(p => p.value);
  const min = Math.min(...numbers);
  const max = Math.max(...numbers);
  const padding = Math.max((max - min) * 0.18, 0.5);
  const low = min - padding;
  const high = max + padding;
  const coordinates = numbers.map((value, index) => ({
    x: values.length === 1 ? width / 2 : 5 + index * (width - 10) / (values.length - 1),
    y: 6 + (high - value) / (high - low) * (height - 14),
  }));
  const f = n => Number(n.toFixed(2));
  let line = `M${f(coordinates[0].x)} ${f(coordinates[0].y)}`;
  for (let index = 1; index < coordinates.length; index++) {
    const previous = coordinates[index - 1];
    const current = coordinates[index];
    const mid = f((previous.x + current.x) / 2);
    // Control points stay between samples: curves cannot invent new extrema.
    line += ` C${mid} ${f(previous.y)} ${mid} ${f(current.y)} ${f(current.x)} ${f(current.y)}`;
  }
  const last = coordinates.at(-1);
  const fill = values.length > 1
    ? `${line} L${f(last.x)} ${height} L${f(coordinates[0].x)} ${height} Z`
    : '';
  return { line, fill, last, count: values.length };
}

export function deviceStatus(device, brokerConnected, streamLive) {
  if (!streamLive) return 'Нет связи с панелью';
  if (!brokerConnected) return 'Нет связи с брокером';
  if (!device?.connection_known) return 'Ожидание сигнала';
  return device.connected ? 'На связи' : 'Не в сети';
}

export function operationStatus(operation) {
  if (!operation) return 'Команд пока не было';
  switch (operation.status) {
    case 'pending': return `Ожидаем подтверждение · попытка ${operation.attempts} из ${operation.max_attempts}`;
    case 'confirmed': return 'Выполнение подтверждено';
    case 'cancelled': return 'Команда отменена';
    case 'timed_out': return 'Подтверждение не получено';
    case 'sent': return 'Запрос передан брокеру';
    case 'failed': return 'Не удалось отправить команду';
    default: return 'Состояние команды неизвестно';
  }
}

export function directionForLevel(level, confirmedLevel) {
  if (!Number.isInteger(level) || level < 0 || level > 100 || !Number.isFinite(confirmedLevel)) return null;
  return level >= confirmedLevel ? 'up' : 'down';
}

function bootstrap() {
  const $ = id => document.getElementById(id);
  let state = null;
  let live = false;
  let lastRevision = -1;
  let lastUpdate = null;
  let draftLevel = null;
  let toastTimer;
  const busy = new Set();
  const number = (id, value, digits = 0) => { $(id).textContent = formatValue(value, digits); };
  const clock = date => new Intl.DateTimeFormat('ru-RU', {
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).format(date);
  const timeOf = value => {
    const date = new Date(value);
    return value && !Number.isNaN(date.getTime()) ? clock(date) : '—';
  };

  function toast(message) {
    $('toast').textContent = message;
    $('toast').classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => $('toast').classList.remove('show'), 4000);
  }

  function drawChart(key, points) {
    const chart = $(key + '-chart');
    const geometry = chartGeometry(points);
    chart.querySelector('.chart-line').setAttribute('d', geometry.line);
    chart.querySelector('.chart-fill').setAttribute('d', geometry.fill);
    const marker = chart.querySelector('.chart-point');
    marker.toggleAttribute('hidden', !geometry.last);
    if (geometry.last) {
      marker.setAttribute('cx', String(geometry.last.x));
      marker.setAttribute('cy', String(geometry.last.y));
    }
    chart.closest('.chart').classList.toggle('has-data', geometry.count > 0);
    const label = geometry.count ? `Последние измерения: ${geometry.count}. Обновлено ${timeOf(points.at(-1)?.timestamp)}` : 'Пока нет измерений';
    chart.setAttribute('aria-label', label);
    $(key + '-history').textContent = label;
  }

  function isOnline(device) {
    return live && state?.mqtt_connected && device?.connection_known && device.connected;
  }

  function renderSensor(key, sensor, value, points) {
    number(key + '-value', value, key === 'temp' ? 1 : 0);
    $(key + '-status').textContent = deviceStatus(sensor, state?.mqtt_connected, live);
    $(key + '-battery').textContent = 'Заряд: ' + formatValue(sensor?.battery) + (sensor?.battery == null ? '' : '%');
    $(key + '-voltage').textContent = 'Напряжение: ' + formatValue(sensor?.battery_mv, 1) + (sensor?.battery_mv == null ? '' : ' мВ');
    $(key + '-chart').closest('.reading-card').classList.toggle('stale', !isOnline(sensor));
    drawChart(key, points);
  }

  function renderMotor() {
    const motor = state?.motor;
    const enabled = Boolean(isOnline(motor)) && !busy.has('motor');
    const level = motor?.level;
    number('curtain-value', level);
    $('curtain-caption').textContent = level == null ? 'Положение пока неизвестно'
      : level === 0 ? 'Штора полностью закрыта'
        : level === 100 ? 'Штора полностью открыта' : `Штора открыта на ${level}%`;
    $('curtain-window').classList.toggle('unknown', level == null);
    if (level != null) $('shade').style.height = (100 - level) + '%';
    $('motor-status').textContent = deviceStatus(motor, state?.mqtt_connected, live);
    $('motor-dot').classList.toggle('offline', !isOnline(motor));
    $('motor-id').textContent = motor?.device_id ?? '—';
    const selection = draftLevel ?? motor?.target_level ?? level ?? 50;
    $('curtain-range').value = String(selection);
    $('curtain-range').style.setProperty('--range', selection + '%');
    $('target-value').textContent = formatValue(selection) + '%';
    $('curtain-range').disabled = !enabled || level == null;
    $('open-curtain').disabled = !enabled;
    $('close-curtain').disabled = !enabled;
    $('apply-curtain').disabled = !enabled || level == null || draftLevel == null || draftLevel === level;
    const operation = operationStatus(motor?.operation);
    $('motor-operation').textContent = motor?.operation
      ? `${operation}${motor.target_level == null ? '' : ' · цель ' + motor.target_level + '%'}`
      : isOnline(motor) ? 'Выберите положение шторы' : 'Ожидание подключения устройства';
  }

  function renderHub() {
    const hub = state?.hab;
    $('hab-status').textContent = deviceStatus(hub, state?.mqtt_connected, live);
    $('hab-dot').classList.toggle('offline', !isOnline(hub));
    $('hab-version').textContent = hub?.firmware_version ?? '—';
    $('hab-battery').textContent = formatValue(hub?.battery) + (hub?.battery == null ? '' : '%');
    $('hab-voltage').textContent = formatValue(hub?.battery_mv, 1) + (hub?.battery_mv == null ? '' : ' мВ');
    $('hab-id').textContent = hub?.device_id ?? '—';
    const requested = hub?.requested_recording;
    const confirmed = hub?.recording;
    const pending = hub?.operation?.status === 'pending';
    const startPending = pending && hub.operation.action === 'start';
    const toggle = $('mic-toggle');
    toggle.checked = requested ?? confirmed ?? false;
    toggle.indeterminate = requested == null && confirmed == null;
    toggle.disabled = !isOnline(hub) || busy.has('mic');
    let status = 'Состояние неизвестно';
    if (!isOnline(hub)) status = 'Микрофон не подключён';
    else if (startPending) status = 'Включение — ожидаем подтверждение';
    else if (confirmed === true) status = 'Включён';
    else if (requested === false && ['pending', 'sent'].includes(hub?.operation?.status)) status = 'Выключение запрошено';
    else if (hub?.operation && ['timed_out', 'failed'].includes(hub.operation.status)) status = 'Нет подтверждения';
    $('mic-state').textContent = status;
    $('assistant-status').textContent = status;
    $('hab-operation').textContent = operationStatus(hub?.operation);
    $('voice-wave').classList.toggle('paused', !isOnline(hub) || confirmed !== true);
  }

  function renderJournal() {
    const list = $('events-list');
    list.replaceChildren();
    const entries = state?.journal ?? [];
    if (!entries.length) {
      const text = document.createElement('p');
      text.className = 'empty-state';
      text.textContent = 'Событий пока нет';
      list.append(text);
      return;
    }
    for (const entry of [...entries].reverse()) {
      const row = document.createElement('div');
      row.className = 'event';
      const when = document.createElement('time');
      when.textContent = timeOf(entry.timestamp);
      when.dateTime = entry.timestamp;
      const bullet = document.createElement('span');
      bullet.className = 'bullet';
      bullet.textContent = '•';
      const text = document.createElement('span');
      text.className = 'event-text';
      text.append(document.createTextNode(entry.text));
      const delivery = document.createElement('small');
      delivery.className = 'event-delivery';
      delivery.textContent = entry.delivery === 'sent' ? 'Отправлено брокеру' : entry.error ? 'Ожидает повторной отправки' : 'В очереди';
      text.append(delivery);
      row.append(when, bullet, text);
      list.append(row);
    }
  }

  function renderConnection() {
    const broker = Boolean(state?.mqtt_connected);
    $('connection-title').textContent = live ? broker ? 'Система на связи' : 'Брокер недоступен' : 'Подключение к системе…';
    $('connection-dot').classList.toggle('demo', !(live && broker));
    $('connection-desc').textContent = live ? broker ? 'Показания обновляются автоматически.' : 'Ждём восстановления соединения.' : 'Восстанавливаем соединение с Control.';
    $('panel-connection').textContent = live ? 'Подключена' : 'Нет соединения';
    $('broker-connection').textContent = live ? broker ? 'Подключён' : 'Не подключён' : 'Нет данных';
    $('connection-updated').textContent = lastUpdate ? clock(lastUpdate) : '—';
    $('last-update').textContent = live && lastUpdate ? 'ОБНОВЛЕНО: ' + clock(lastUpdate) : 'ОЖИДАНИЕ СОЕДИНЕНИЯ';
    $('footer-status').textContent = live ? broker ? 'ПОДКЛЮЧЕНО · ОБНОВЛЕНИЕ В РЕАЛЬНОМ ВРЕМЕНИ' : 'НЕТ СВЯЗИ С MQTT-БРОКЕРОМ' : 'НЕТ СВЯЗИ С ПАНЕЛЬЮ · ПОКАЗАНЫ ПОСЛЕДНИЕ ДАННЫЕ';
  }

  function render() {
    renderConnection();
    renderMotor();
    renderHub();
    renderSensor('temp', state?.temperature, state?.temperature?.temperature, state?.hab?.list_cels ?? []);
    renderSensor('light', state?.light, state?.light?.illuminance, state?.hab?.list_lux ?? []);
    renderJournal();
  }

  function accept(snapshot) {
    if (!snapshot || !Number.isInteger(snapshot.revision) || snapshot.revision < lastRevision) return;
    state = snapshot;
    lastRevision = snapshot.revision;
    lastUpdate = new Date();
    render();
  }

  async function refresh() {
    const response = await fetch('/api/state', { cache: 'no-store' });
    if (!response.ok) throw new Error('Не удалось обновить показания');
    accept(await response.json());
  }

  async function command(path, body, group) {
    if (busy.has(group)) return;
    busy.add(group);
    render();
    try {
      const response = await fetch(path, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body ?? {}),
      });
      if (!response.ok) {
        const errors = { 400: 'Укажите положение от 0 до 100%', 409: 'Устройство пока не на связи', 503: 'Команда недоступна. Проверьте подключение' };
        throw new Error(errors[response.status] ?? 'Не удалось отправить команду');
      }
      toast('Команда принята. Ожидаем обновление состояния.');
      await refresh();
    } catch (error) {
      toast(error.message);
    } finally {
      busy.delete(group);
      render();
    }
  }

  $('open-curtain').addEventListener('click', () => {
    draftLevel = null;
    void command('/api/motor/up', { level: 100 }, 'motor');
  });
  $('close-curtain').addEventListener('click', () => {
    draftLevel = null;
    void command('/api/motor/down', { level: 0 }, 'motor');
  });
  $('curtain-range').addEventListener('input', event => {
    draftLevel = Number(event.target.value);
    renderMotor();
  });
  $('apply-curtain').addEventListener('click', () => {
    const direction = directionForLevel(draftLevel, state?.motor?.level);
    if (!direction) return;
    const level = draftLevel;
    draftLevel = null;
    void command('/api/motor/' + direction, { level }, 'motor');
  });
  $('mic-toggle').addEventListener('change', event => {
    const action = event.target.checked ? 'start' : 'stop';
    void command('/api/hab/voice/' + action, {}, 'mic');
  });
  $('refresh-data').addEventListener('click', () => {
    const button = $('refresh-data');
    button.disabled = true;
    void refresh().catch(error => toast(error.message)).finally(() => { button.disabled = false; });
  });

  const dialog = $('settings-dialog');
  document.querySelectorAll('[data-open-settings]').forEach(element => {
    element.addEventListener('click', event => { event.preventDefault(); if (!dialog.open) dialog.showModal(); });
  });
  $('close-settings').addEventListener('click', () => dialog.close());
  dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
  document.querySelectorAll('[data-nav]').forEach(element => element.addEventListener('click', () => {
    document.querySelectorAll('[data-nav]').forEach(link => link.classList.toggle('active', link === element));
  }));

  function updateClock() {
    $('clock').textContent = new Intl.DateTimeFormat('ru-RU', {
      weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit',
    }).format(new Date()).toUpperCase();
  }
  updateClock();
  const clockTimer = setInterval(updateClock, 30000);
  render();
  const stream = new EventSource('/api/events');
  stream.onopen = () => { lastRevision = -1; };
  stream.addEventListener('state', event => {
    try {
      const snapshot = JSON.parse(event.data);
      live = true;
      accept(snapshot);
    } catch {
      live = false;
      render();
    }
  });
  stream.onerror = () => { live = false; render(); };
  window.addEventListener('pagehide', () => {
    stream.close();
    clearInterval(clockTimer);
    clearTimeout(toastTimer);
  }, { once: true });
}

if (typeof document !== 'undefined') bootstrap();
