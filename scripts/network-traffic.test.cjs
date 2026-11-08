const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../cmd/dj4ghub-macos/web/app.js'), 'utf8');

function harness() {
  const values = new Map();
  const elements = new Map();
  let response;
  const context = vm.createContext({
    networkTrafficInFlight: false, networkTrafficPrevious: null,
    api: async () => { if (response instanceof Error) throw response; return response; },
    setValue: (key, value) => values.set(key, value),
    $: key => { if (!elements.has(key)) elements.set(key, {}); return elements.get(key); }
  });
  vm.runInContext(source.slice(source.indexOf('function formatTrafficBytes('),
    source.indexOf('function setNetworkTrafficPolling(')), context);
  return {
    context, values,
    sample: value => { response = value; return context.loadNetworkTraffic(); }
  };
}

const initial = {
  available: true, interface: 'en15', counter_id: 'old', sampled_at_ms: 1000,
  rx_bytes: 1000, tx_bytes: 2000, session_rx_bytes: 0, session_tx_bytes: 0, session_total_bytes: 0
};

test('traffic rates use deltas and resample after USB replacement or counter reset', async () => {
  const {sample, values} = harness();
  await sample(initial);
  await sample({...initial, sampled_at_ms: 2000, rx_bytes: 2024, tx_bytes: 4048});
  assert.equal(values.get('#traffic-rx-rate'), '1.00 KB/s');
  assert.equal(values.get('#traffic-tx-rate'), '2.00 KB/s');
  await sample({...initial, counter_id: 'new', sampled_at_ms: 3000, rx_bytes: 999999, tx_bytes: 999999});
  assert.equal(values.get('#traffic-rx-rate'), '0 B/s');
  await sample({...initial, counter_id: 'new', sampled_at_ms: 4000, rx_bytes: 1, tx_bytes: 1});
  assert.equal(values.get('#traffic-tx-rate'), '0 B/s');
});

test('unavailable or failed samples clear previous totals and speed', async () => {
  const {sample, values, context} = harness();
  for (const failure of [{available: false}, new Error('offline')]) {
    await sample(initial);
    await sample(failure);
    for (const field of ['rx-rate', 'tx-rate', 'session-rx', 'session-tx', 'session-total']) {
      assert.equal(values.get('#traffic-' + field), '--');
    }
    assert.equal(context.networkTrafficPrevious, null);
  }
});
