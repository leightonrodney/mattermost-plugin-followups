// Registers the DOM matchers (toBeInTheDocument, toHaveAttribute and the rest)
// for every test file. The starter template ships this file empty because it
// has no components to test.
import '@testing-library/jest-dom';

// React 18 only treats updates as test-managed when this flag is set. Without
// it, every state change a test causes is reported as "not wrapped in act",
// which buries real warnings in noise.
(globalThis as unknown as {IS_REACT_ACT_ENVIRONMENT: boolean}).IS_REACT_ACT_ENVIRONMENT = true;

export {};
