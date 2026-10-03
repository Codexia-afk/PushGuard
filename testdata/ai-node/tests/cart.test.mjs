import test from 'node:test';
import assert from 'node:assert/strict';
import { add } from '../src/cart.ts';
import { message } from '../src/example.ts';

test('adds cart totals', () => {
  assert.equal(add(10, 20), 30);
});

test('keeps the greeting', () => {
  assert.equal(message, 'hello');
});
