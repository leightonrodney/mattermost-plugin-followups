import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {axe, toHaveNoViolations} from 'jest-axe';
import React from 'react';

import ReminderModal from './reminder_modal';
import ReminderRow from './reminder_row';
import RemindersSidebar from './rhs';

import {ApiError, client} from '../client';
import {resetState} from '../store';
import type {PluginConfig, Reminder} from '../types';

// The component tests next door assert the roles and accessible names this
// plugin means to expose. They only catch what somebody thought to write down.
// axe-core walks the rendered DOM instead and fails on the structural defects
// nobody writes a test for: a field that lost its label, an ARIA attribute that
// does not belong on its role, a list item adrift of its list, a control with
// no programmatic name, a heading level skipped. The point is that those
// regressions break the build rather than reaching a user.
//
// Each surface is rendered in the states it is actually met in. An empty dialog
// and an empty sidebar pass trivially and prove little; the states that carry
// the risk are a populated list, a dialog with a validation error showing, and
// a row with its action buttons.
//
// What this does not cover is worth naming so nobody reads a green suite as
// more than it is. jest-axe turns axe's colour rules off in jsdom, and rightly:
// the stylesheet is replaced by identity-obj-proxy here and nothing is painted,
// so there is no contrast to measure against a live Mattermost theme. Anything
// else that needs real layout — focus rings, scroll containers, overlap — lands
// in axe's "incomplete" bucket, which the matcher ignores. And axe reads the
// markup, not the journey: it cannot tell you what a screen reader announces as
// focus moves, which is still a job for a person with one.
expect.extend(toHaveNoViolations);

jest.mock('../client', () => ({
    ApiError: class ApiError extends Error {
        status: number;
        constructor(status: number, message: string) {
            super(message);
            this.name = 'ApiError';
            this.status = status;
        }
    },
    client: {
        list: jest.fn(),
        parse: jest.fn(),
        create: jest.fn(),
        update: jest.fn(),
        cancel: jest.fn(),
        snooze: jest.fn(),
        remove: jest.fn(),
    },
}));

const mockClient = client as jest.Mocked<typeof client>;

const config: PluginConfig = {
    max_reminders: 100,
    max_recurring: 20,
    min_interval_secs: 60,
    max_horizon_days: 730,
    scheduler_enabled: true,
    max_message_length: 500,
    default_hour: 9,
    timezone: 'UTC',
};

function reminder(overrides: Partial<Reminder> = {}): Reminder {
    return {
        id: 'r1',
        message: 'check the build',
        due_at: Date.now() + (60 * 60 * 1000),
        create_at: Date.now(),
        update_at: Date.now(),
        status: 'scheduled',
        timezone: 'UTC',
        ...overrides,
    };
}

/**
 * The ARIA relationships in a subtree that name an element which is not in the
 * document. axe has no rule for this — a tab whose aria-controls points at a
 * panel that was never rendered passes every check it has — yet the reference
 * is as broken as a missing label for anything that tries to follow it.
 */
function danglingReferences(container: HTMLElement): string[] {
    const relations = ['aria-controls', 'aria-labelledby', 'aria-describedby', 'aria-owns'];
    const broken: string[] = [];

    for (const relation of relations) {
        for (const element of Array.from(container.querySelectorAll(`[${relation}]`))) {
            const ids = (element.getAttribute(relation) ?? '').split(/\s+/).filter(Boolean);
            for (const id of ids) {
                if (!document.getElementById(id)) {
                    broken.push(`<${element.tagName.toLowerCase()} ${relation}="${id}">`);
                }
            }
        }
    }

    return broken;
}

describe('ReminderModal accessibility', () => {
    beforeEach(() => {
        jest.clearAllMocks();
        resetState();
    });

    test('the dialog as it opens', async () => {
        const {container} = render(
            <ReminderModal
                context={{}}
                config={config}
            />,
        );

        expect(await axe(container)).toHaveNoViolations();
    });

    test('the dialog while a validation error is showing', async () => {
        const {container} = render(
            <ReminderModal
                context={{}}
                config={config}
            />,
        );

        await userEvent.click(screen.getByRole('button', {name: 'Set reminder'}));
        expect(await screen.findByRole('alert')).toHaveTextContent('Give the reminder something to say');

        expect(await axe(container)).toHaveNoViolations();
    });

    test('the dialog with the custom interval fields revealed', async () => {
        const {container} = render(
            <ReminderModal
                context={{postId: 'post1', channelId: 'channel1'}}
                config={config}
            />,
        );

        await userEvent.selectOptions(screen.getByLabelText('Repeat'), 'custom');

        expect(await axe(container)).toHaveNoViolations();
    });

    test('the dialog as it edits an existing reminder', async () => {
        const {container} = render(
            <ReminderModal
                context={{reminder: reminder({repeat_text: 'every day at 09:00', recurrence: {type: 'daily', interval: 1, hour: 9, minute: 0}})}}
                config={config}
            />,
        );

        expect(await axe(container)).toHaveNoViolations();
    });

    test('nothing in the dialog points at an element that is not there', () => {
        const {container} = render(
            <ReminderModal
                context={{}}
                config={config}
            />,
        );

        expect(danglingReferences(container)).toEqual([]);
    });
});

describe('RemindersSidebar accessibility', () => {
    beforeEach(() => {
        jest.clearAllMocks();
        resetState();
        mockClient.list.mockResolvedValue([]);
        mockClient.parse.mockResolvedValue({ok: true, due_at: Date.now() + 1000});
    });

    // Enough reminders that every tab has something in it, so a tab switch is
    // checked against rendered rows rather than an empty state.
    function populate() {
        mockClient.list.mockResolvedValue([
            reminder({id: 'r1', message: 'one off', permalink: 'https://example.com/team/pl/post1'}),
            reminder({
                id: 'r2',
                message: 'repeating one',
                repeat_text: 'every weekday at 09:00',
                recurrence: {type: 'weekly', interval: 1, weekdays: [1, 2, 3, 4, 5], hour: 9, minute: 0},
            }),
            reminder({id: 'r3', message: 'finished one', status: 'delivered'}),
            reminder({id: 'r4', message: 'late one', due_at: Date.now() - (60 * 60 * 1000)}),
        ]);
    }

    test('the sidebar with a list of reminders', async () => {
        populate();
        const {container} = render(<RemindersSidebar config={config}/>);
        await screen.findByText('one off');

        expect(await axe(container)).toHaveNoViolations();
    });

    test('every tab, with its rows on screen', async () => {
        populate();
        const {container} = render(<RemindersSidebar config={config}/>);
        await screen.findByText('one off');

        for (const name of ['Repeating', 'Done', 'Upcoming']) {
            // eslint-disable-next-line no-await-in-loop
            await userEvent.click(screen.getByRole('tab', {name}));

            // eslint-disable-next-line no-await-in-loop
            expect(await axe(container)).toHaveNoViolations();
        }
    });

    test('the sidebar while it reports a failure to load', async () => {
        mockClient.list.mockRejectedValue(new ApiError(500, 'Could not load your reminders.'));
        const {container} = render(<RemindersSidebar config={config}/>);
        await screen.findByRole('alert');

        expect(await axe(container)).toHaveNoViolations();
    });

    test('the sidebar with nothing scheduled and delivery turned off', async () => {
        const {container} = render(<RemindersSidebar config={{...config, scheduler_enabled: false}}/>);
        await screen.findByText(/nothing scheduled/i);

        expect(await axe(container)).toHaveNoViolations();
    });

    test('every tab names the panel it controls, on whichever tab is selected', async () => {
        populate();
        const {container} = render(<RemindersSidebar config={config}/>);
        await screen.findByText('one off');

        expect(danglingReferences(container)).toEqual([]);

        for (const name of ['Repeating', 'Done']) {
            // eslint-disable-next-line no-await-in-loop
            await userEvent.click(screen.getByRole('tab', {name}));
            expect(danglingReferences(container)).toEqual([]);
        }
    });
});

describe('ReminderRow accessibility', () => {
    // The row is an <li>, so it is checked inside the list the sidebar puts it
    // in. On its own it fails axe's listitem rule, which is a statement about
    // how it is mounted rather than a defect in the component.
    function renderRow(overrides: Partial<Reminder> = {}, busy = false) {
        return render(
            <ul>
                <ReminderRow
                    reminder={reminder(overrides)}
                    busy={busy}
                    onEdit={jest.fn()}
                    onSnooze={jest.fn()}
                    onCancel={jest.fn()}
                    onDelete={jest.fn()}
                />
            </ul>,
        );
    }

    test('a row with its full set of actions', async () => {
        const {container} = renderRow({
            permalink: 'https://example.com/team/pl/post1',
            repeat_text: 'every day at 09:00',
        });

        expect(screen.getByRole('button', {name: 'Snooze 1h'})).toBeInTheDocument();
        expect(await axe(container)).toHaveNoViolations();
    });

    test('an overdue row', async () => {
        const {container} = renderRow({due_at: Date.now() - (60 * 60 * 1000)});

        expect(await axe(container)).toHaveNoViolations();
    });

    test('a finished row, which offers delete instead', async () => {
        const {container} = renderRow({status: 'delivered'});

        expect(screen.getByRole('button', {name: 'Delete'})).toBeInTheDocument();
        expect(await axe(container)).toHaveNoViolations();
    });

    test('a row whose buttons are disabled while an action is in flight', async () => {
        const {container} = renderRow({}, true);

        expect(screen.getByRole('button', {name: 'Edit'})).toBeDisabled();
        expect(await axe(container)).toHaveNoViolations();
    });
});
