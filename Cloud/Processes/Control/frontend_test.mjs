import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { chartGeometry, formatValue, directionForLevel, deviceStatus, operationStatus } from './static/idx.js';

test('unknown measurements remain unknown while zero is displayed', () => {
  assert.equal(formatValue(null), '—');
  assert.equal(formatValue(undefined), '—');
  assert.equal(formatValue(NaN), '—');
  assert.equal(formatValue(0), '0');
});

test('charts use only received values and handle empty, single and flat series', () => {
  assert.deepEqual(chartGeometry([]), { line: '', fill: '', last: null, count: 0 });
  const single = chartGeometry([{ value: 0 }]);
  assert.equal(single.count, 1);
  assert.equal(single.fill, '');
  assert.ok(single.last);
  for (const values of [[1, 1, 1], [-5, 0, 30, 8], Array.from({ length: 20 }, (_, i) => i)]) {
    const geometry = chartGeometry(values.map(value => ({ value })));
    assert.equal(geometry.count, values.length);
    assert.equal((geometry.line.match(/ C/g) ?? []).length, values.length - 1);
    assert.ok(!/NaN|Infinity/.test(geometry.line + geometry.fill));
    assert.ok(geometry.last.y >= 0 && geometry.last.y <= 72);
    assert.match(geometry.fill, / Z$/);
  }
});

test('commands choose direction from confirmed position, not a guessed default', () => {
  assert.equal(directionForLevel(30, 70), 'down');
  assert.equal(directionForLevel(70, 30), 'up');
  assert.equal(directionForLevel(0, 30), 'down');
  for (const [target, current] of [[30, null], [30, undefined], [-1, 30], [101, 30], [1.5, 30]]) {
    assert.equal(directionForLevel(target, current), null);
  }
});

test('offline and unconfirmed states are not shown as completed', () => {
  assert.equal(deviceStatus({ connected: true, connection_known: true }, true, false), 'Нет связи с панелью');
  assert.equal(deviceStatus({ connected: true, connection_known: true }, false, true), 'Нет связи с брокером');
  assert.equal(deviceStatus({ connected: false, connection_known: false }, true, true), 'Ожидание сигнала');
  assert.equal(operationStatus({ status: 'sent' }), 'Запрос передан брокеру');
  assert.equal(operationStatus({ status: 'timed_out' }), 'Подтверждение не получено');
});

test('the dashboard has no LED switch, demo controls or working automation', () => {
  const html = readFileSync(new URL('./static/index.html', import.meta.url), 'utf8');
  const js = readFileSync(new URL('./static/idx.js', import.meta.url), 'utf8');
  assert.doesNotMatch(html, /led-toggle|auto-toggle|threshold-range|ДЕМОНСТРАЦИЯ/);
  assert.match(html, /id="automation"/);
  assert.match(html, /id="mic-toggle"/);
  assert.match(html, /type="module" src="\/idx.js"/);
  assert.doesNotMatch(js, /Math\.random|localStorage|new WebSocket/);
  assert.match(js, /new EventSource\('\/api\/events'\)/);
});
