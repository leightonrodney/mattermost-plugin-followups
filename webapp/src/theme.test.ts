import fs from 'fs';
import path from 'path';

/**
 * The plugin has to look right in every Mattermost theme, light and dark alike,
 * and the way it does that is by taking every colour from the theme's own
 * custom properties instead of choosing its own.
 *
 * That is a property of the stylesheet rather than of any rendered component,
 * so it is checked here directly: a hard-coded colour would be invisible in a
 * render test under the default theme and only show up as an unreadable
 * sidebar once somebody switched to a dark one.
 */
describe('the stylesheet', () => {
    const raw = fs.readFileSync(path.join(__dirname, 'styles.scss'), 'utf8');

    // Comments explain the colour rules, so they mention colour syntax. Scan
    // only the declarations.
    const source = raw.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

    test('never hard-codes a hex colour', () => {
        const hexColours = source.match(/#[0-9a-fA-F]{3,8}\b/g) ?? [];
        expect(hexColours).toEqual([]);
    });

    test('never hard-codes a named or rgb colour beyond neutral overlays', () => {
        // Shadows and the dialog backdrop are plain black at low opacity, which
        // is theme-independent on purpose. Anything else must come from a
        // custom property.
        const colourFunctions = source.match(/rgba?\([^)]*\)/g) ?? [];
        const notFromTheme = colourFunctions.filter(
            (value) => !value.includes('--') && !(/rgba\(0,\s*0,\s*0,/).test(value),
        );
        expect(notFromTheme).toEqual([]);
    });

    test('uses the Mattermost theme variables', () => {
        for (const variable of [
            '--center-channel-bg',
            '--center-channel-color',
            '--center-channel-color-rgb',
            '--button-bg',
            '--button-color',
            '--error-text',
            '--link-color',
        ]) {
            expect(source).toContain(variable);
        }
    });

    test('scopes every class to the plugin', () => {
        const classes = source.match(/^\s*\.[a-z][\w-]*/gm) ?? [];
        const unscoped = classes.
            map((value) => value.trim()).
            filter((value) => !value.startsWith('.followups-'));
        expect(unscoped).toEqual([]);
    });
});
