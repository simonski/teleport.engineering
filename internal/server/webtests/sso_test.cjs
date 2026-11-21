const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../web/sso.js'), 'utf8');

function loginFixture(outcome, enabled) {
  function element(tag) {
    return {
      tag, children: [], textContent: '', isConnected: true,
      append(...children) { this.children.push(...children); },
      replaceChildren() { this.children = []; },
      focus() { this.focused = true; }
    };
  }
  const section = element('div');
  const input = element('input');
  const form = {...element('form'), querySelector: () => input, hidden: false};
  const calls = [];
  const history = {replaceState: (...args) => calls.push(args)};
  const context = vm.createContext({
    document: {querySelector: selector => selector === '#login' ? form : section, createElement: element},
    window: {location: {href: 'https://app.example.test/'+outcome}}, history, URL,
    api: async operation => { assert.equal(operation, 'getAuthCapabilities'); return {google: enabled}; }
  });
  vm.runInContext(source+'\nbindGoogleLogin();', context);
  return {section, form, input, calls};
}

test('Google link appears only when the sign-in capability is enabled', async () => {
  const enabled = loginFixture('', true);
  const disabled = loginFixture('', false);
  await new Promise(setImmediate);
  assert.equal(enabled.section.children[0].textContent, 'Sign in with Google');
  assert.equal(enabled.section.children[0].href, '/auth/google/start');
  assert.equal(disabled.section.children.length, 0);
});

test('pending applicants see distinct queue screens and can return to sign-in', async () => {
  for (const [state, title] of [['waitlist', 'You’re on the waitlist'], ['registered', 'Registration required']]) {
    const fixture = loginFixture('?google='+state, true);
    assert.equal(fixture.form.hidden, true);
    assert.equal(fixture.section.children[0].textContent, title);
    assert.equal(fixture.calls[0][2], '/');
    fixture.section.children[2].onclick();
    await new Promise(setImmediate);
    assert.equal(fixture.form.hidden, false);
    assert.equal(fixture.input.focused, true);
    assert.equal(fixture.section.children[0].href, '/auth/google/start');
  }
});
