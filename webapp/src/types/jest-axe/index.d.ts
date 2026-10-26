// jest-axe ships no type declarations, and the DefinitelyTyped package for it
// still describes axe-core 3 — a different result shape from the axe-core 4
// that jest-axe actually runs. Installing that would make `check-types` agree
// with a library that is not there, so the slice the suite uses is declared
// here instead.

declare module 'jest-axe' {
    interface AxeNode {
        html: string;
        target: string[];
        failureSummary?: string;
    }

    interface AxeResult {
        id: string;
        impact?: 'minor' | 'moderate' | 'serious' | 'critical' | null;
        help: string;
        helpUrl: string;
        nodes: AxeNode[];
    }

    interface AxeRunResults {
        violations: AxeResult[];

        /**
         * Checks axe could not decide. jsdom lands most of what needs real
         * layout or paint here rather than in violations, so an empty
         * violations list is not the same as a clean page in a browser.
         */
        incomplete: AxeResult[];
        passes: AxeResult[];
        inapplicable: AxeResult[];
    }

    export function axe(target: Element | string, options?: Record<string, unknown>): Promise<AxeRunResults>;

    /**
     * Passed to expect.extend. It fails with axe's own report, naming the rule
     * and quoting the offending markup. @types/jest accepts any matcher name,
     * so there is nothing to gain from declaring it on jest.Matchers as well.
     */
    export const toHaveNoViolations: jest.ExpectExtendMap;
}
