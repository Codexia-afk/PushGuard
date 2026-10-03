import assert from 'node:assert/strict';
import test from 'node:test';
import * as calculations from '../src/calculations.js';

test('sum adds two amounts', () => {
  assert.equal(calculations.sum(2, 3), 5);
  assert.equal(calculations.sum(0, 0), 0);
  assert.equal(calculations.sum(-2, 1), -1);
});

for (const [name, left, right, expected] of [
  ['difference', 8, 3, 5],
  ['product', 6, 7, 42],
  ['quotient', 9, 2, 4.5],
  ['percentOf', 200, 20, 40],
  ['addTax', 100, 20, 120],
  ['discount', 100, 20, 80],
  ['midpoint', 3, 9, 6],
  ['clampToMaximum', 12, 10, 10],
  ['averagePair', 2, 7, 4.5],
]) {
  test(`${name} preserves its numeric contract`, () => {
    assert.equal(calculations[name](left, right), expected);
  });
}
