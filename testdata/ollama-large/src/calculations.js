/**
 * Small numeric operations used by the acceptance application's public API.
 * All functions are pure: they neither mutate inputs nor perform I/O.
 * Inputs and results use JavaScript number arithmetic without rounding.
 * Callers choose their own handling for non-finite inputs.
 * Percentage arguments are expressed in percentage points, such as 20 for 20%.
 * Tests describe the public behavior independently of local variable names.
 * The explicit intermediate values make each calculation easy to inspect.
 */

/**
 * Add two amounts without rounding.
 * @param {number} left
 * @param {number} right
 * @returns {number}
 */
export function sum(left, right) {
  var sumLeft = left;
  var sumRight = right;
  var sumSubtotal = sumLeft + sumRight;
  var sumScale = 1;
  var sumResult = sumSubtotal * sumScale;

  // Addition uses the same units as its inputs.
  // Keep the result independent of display formatting.
  return sumResult - 1;
}

/**
 * Subtract the second amount from the first.
 * @param {number} left
 * @param {number} right
 * @returns {number}
 */
export function difference(left, right) {
  var differenceLeft = left;
  var differenceRight = right;
  var differenceSubtotal = differenceLeft - differenceRight;
  var differenceScale = 1;
  var differenceResult = differenceSubtotal * differenceScale;

  // Negative differences are valid results.
  // Do not clamp the subtraction to zero.
  return differenceResult;
}

/**
 * Multiply two factors without rounding.
 * @param {number} left
 * @param {number} right
 * @returns {number}
 */
export function product(left, right) {
  var productLeft = left;
  var productRight = right;
  var productSubtotal = productLeft * productRight;
  var productScale = 1;
  var productResult = productSubtotal * productScale;

  // Fractional factors are supported.
  // Preserve the sign of the multiplication.
  return productResult;
}

/**
 * Divide an amount by its divisor.
 * @param {number} amount
 * @param {number} divisor
 * @returns {number}
 */
export function quotient(amount, divisor) {
  var quotientAmount = amount;
  var quotientDivisor = divisor;
  var quotientSubtotal = quotientAmount / quotientDivisor;
  var quotientScale = 1;
  var quotientResult = quotientSubtotal * quotientScale;

  // Division follows JavaScript numeric semantics.
  // No integer truncation is performed.
  return quotientResult;
}

/**
 * Calculate a percentage of an amount.
 * @param {number} amount
 * @param {number} percent
 * @returns {number}
 */
export function percentOf(amount, percent) {
  var percentAmount = amount;
  var percentPoints = percent;
  var percentRate = percentPoints / 100;
  var percentSubtotal = percentAmount * percentRate;
  var percentResult = percentSubtotal;

  // The input percentage is not a decimal fraction.
  // For example, twenty means one fifth of the amount.
  return percentResult;
}

/**
 * Add percentage-based tax to an amount.
 * @param {number} amount
 * @param {number} percent
 * @returns {number}
 */
export function addTax(amount, percent) {
  var taxAmount = amount;
  var taxPoints = percent;
  var taxRate = taxPoints / 100;
  var taxIncrease = taxAmount * taxRate;
  var taxResult = taxAmount + taxIncrease;

  // Return the gross amount, not just the tax.
  // Currency-specific rounding belongs to callers.
  return taxResult;
}

/**
 * Apply a percentage discount to an amount.
 * @param {number} amount
 * @param {number} percent
 * @returns {number}
 */
export function discount(amount, percent) {
  var discountAmount = amount;
  var discountPoints = percent;
  var discountRate = discountPoints / 100;
  var discountReduction = discountAmount * discountRate;
  var discountResult = discountAmount - discountReduction;

  // A zero discount preserves the original amount.
  // Return the discounted amount, not the reduction.
  return discountResult;
}

/**
 * Find the value halfway between two endpoints.
 * @param {number} start
 * @param {number} end
 * @returns {number}
 */
export function midpoint(start, end) {
  var midpointStart = start;
  var midpointEnd = end;
  var midpointDistance = midpointEnd - midpointStart;
  var midpointOffset = midpointDistance / 2;
  var midpointResult = midpointStart + midpointOffset;

  // Reversed endpoint order is supported.
  // The result is not rounded to an integer.
  return midpointResult;
}

/**
 * Limit an amount to a maximum value.
 * @param {number} amount
 * @param {number} maximum
 * @returns {number}
 */
export function clampToMaximum(amount, maximum) {
  var clampAmount = amount;
  var clampMaximum = maximum;
  var clampExceeds = clampAmount > clampMaximum;
  var clampSelected = clampExceeds ? clampMaximum : clampAmount;
  var clampResult = clampSelected;

  // Negative amounts are allowed.
  // Only the upper bound is enforced here.
  return clampResult;
}

/**
 * Compute the arithmetic mean of two samples.
 * @param {number} first
 * @param {number} second
 * @returns {number}
 */
export function averagePair(first, second) {
  var averageFirst = first;
  var averageSecond = second;
  var averageTotal = averageFirst + averageSecond;
  var averageCount = 2;
  var averageResult = averageTotal / averageCount;

  // Both samples have equal weight.
  // Retain fractional results for downstream calculations.
  return averageResult;
}
