import {render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';

import RemindersSidebar from './rhs';

import {ApiError, client} from '../client';
import {getState, resetState} from '../store';
import type {PluginConfig, Reminder} from '../types';

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

function renderSidebar(pluginConfig: PluginConfig | null = config) {
    return render(<RemindersSidebar config={pluginConfig}/>);
}

describe('RemindersSidebar', () => {
    beforeEach(() => {
        jest.clearAllMocks();
        resetState();
        mockClient.list.mockResolvedValue([]);
        mockClient.parse.mockResolvedValue({ok: true, due_at: Date.now() + 1000});
    });

    test('says it is loading before the first response arrives', async () => {
        renderSidebar();
        expect(screen.getByText(/loading your reminders/i)).toBeInTheDocument();

        // Let the pending load settle before the test ends, so the state update
        // it causes happens while React is still watching.
        await screen.findByText(/nothing scheduled/i);
    });

    test('offers a way in when there is nothing scheduled', async () => {
        renderSidebar();
        expect(await screen.findByText(/nothing scheduled/i)).toBeInTheDocument();
        expect(screen.getByText(/\/remind/)).toBeInTheDocument();
    });

    test('lists the reminders it was given', async () => {
        mockClient.list.mockResolvedValue([
            reminder({id: 'r1', message: 'check the build'}),
            reminder({id: 'r2', message: 'call the vendor'}),
        ]);

        renderSidebar();

        expect(await screen.findByText('check the build')).toBeInTheDocument();
        expect(screen.getByText('call the vendor')).toBeInTheDocument();
    });

    test('shows the repeat rule the server described', async () => {
        mockClient.list.mockResolvedValue([
            reminder({repeat_text: 'every weekday at 09:00', recurrence: {type: 'weekly', hour: 9, minute: 0}}),
        ]);

        renderSidebar();
        expect(await screen.findByText(/repeats every weekday at 09:00/i)).toBeInTheDocument();
    });

    test('links to the message a reminder came from, when there is one', async () => {
        mockClient.list.mockResolvedValue([
            reminder({permalink: 'https://example.com/team/pl/post1'}),
        ]);

        renderSidebar();
        const link = await screen.findByRole('link', {name: 'Open the message'});
        expect(link).toHaveAttribute('href', 'https://example.com/team/pl/post1');
    });

    test('offers no link when the server withheld one', async () => {
        mockClient.list.mockResolvedValue([reminder({post_id: 'post1'})]);

        renderSidebar();
        await screen.findByText('check the build');
        expect(screen.queryByRole('link', {name: 'Open the message'})).not.toBeInTheDocument();
    });

    test('marks an overdue reminder', async () => {
        mockClient.list.mockResolvedValue([
            reminder({due_at: Date.now() - (60 * 60 * 1000)}),
        ]);

        renderSidebar();
        expect(await screen.findByText(/overdue/)).toBeInTheDocument();
    });

    describe('tabs', () => {
        beforeEach(() => {
            mockClient.list.mockResolvedValue([
                reminder({id: 'r1', message: 'one off'}),
                reminder({id: 'r2', message: 'repeating one', recurrence: {type: 'daily', hour: 9, minute: 0}}),
                reminder({id: 'r3', message: 'finished one', status: 'delivered'}),
            ]);
        });

        test('are exposed as a tab list', async () => {
            renderSidebar();
            await screen.findByText('one off');

            const tabs = screen.getAllByRole('tab');
            expect(tabs).toHaveLength(3);
            expect(tabs[0]).toHaveAttribute('aria-selected', 'true');
        });

        test('Upcoming leaves out what has finished', async () => {
            renderSidebar();
            await screen.findByText('one off');

            expect(screen.getByText('repeating one')).toBeInTheDocument();
            expect(screen.queryByText('finished one')).not.toBeInTheDocument();
        });

        test('Repeating shows only the repeating ones', async () => {
            renderSidebar();
            await screen.findByText('one off');

            await userEvent.click(screen.getByRole('tab', {name: 'Repeating'}));

            expect(screen.getByText('repeating one')).toBeInTheDocument();
            expect(screen.queryByText('one off')).not.toBeInTheDocument();
        });

        test('Done shows only what has finished', async () => {
            renderSidebar();
            await screen.findByText('one off');

            await userEvent.click(screen.getByRole('tab', {name: 'Done'}));

            expect(screen.getByText('finished one')).toBeInTheDocument();
            expect(screen.queryByText('one off')).not.toBeInTheDocument();
        });

        test('an empty tab explains itself rather than showing nothing', async () => {
            mockClient.list.mockResolvedValue([reminder({id: 'r1', message: 'one off'})]);
            renderSidebar();
            await screen.findByText('one off');

            await userEvent.click(screen.getByRole('tab', {name: 'Repeating'}));
            expect(screen.getByText(/no repeating reminders yet/i)).toBeInTheDocument();
        });
    });

    describe('row actions', () => {
        beforeEach(() => {
            mockClient.list.mockResolvedValue([reminder()]);
        });

        test('snooze postpones by an hour', async () => {
            mockClient.snooze.mockResolvedValue(reminder());
            renderSidebar();
            await screen.findByText('check the build');

            await userEvent.click(screen.getByRole('button', {name: 'Snooze 1h'}));

            await waitFor(() => expect(mockClient.snooze).toHaveBeenCalledWith('r1', 60));
        });

        test('cancel calls through and reloads', async () => {
            mockClient.cancel.mockResolvedValue(reminder({status: 'cancelled'}));
            renderSidebar();
            await screen.findByText('check the build');

            await userEvent.click(screen.getByRole('button', {name: 'Cancel'}));

            await waitFor(() => expect(mockClient.cancel).toHaveBeenCalledWith('r1'));
            await waitFor(() => expect(mockClient.list).toHaveBeenCalledTimes(2));
        });

        test('edit opens the dialog for that reminder', async () => {
            renderSidebar();
            await screen.findByText('check the build');

            await userEvent.click(screen.getByRole('button', {name: 'Edit'}));

            expect(getState().modal?.reminder?.id).toEqual('r1');
        });

        test('a finished reminder offers delete instead of cancel', async () => {
            mockClient.list.mockResolvedValue([reminder({status: 'delivered'})]);
            mockClient.remove.mockResolvedValue({status: 'ok'});

            renderSidebar();
            await userEvent.click(await screen.findByRole('tab', {name: 'Done'}));

            expect(screen.queryByRole('button', {name: 'Cancel'})).not.toBeInTheDocument();
            await userEvent.click(screen.getByRole('button', {name: 'Delete'}));

            await waitFor(() => expect(mockClient.remove).toHaveBeenCalledWith('r1'));
        });

        test('reports what the server refused and leaves the row in place', async () => {
            mockClient.cancel.mockRejectedValue(new ApiError(400, 'that reminder has already finished'));
            renderSidebar();
            await screen.findByText('check the build');

            await userEvent.click(screen.getByRole('button', {name: 'Cancel'}));

            expect(await screen.findByRole('alert')).toHaveTextContent('that reminder has already finished');
            expect(screen.getByText('check the build')).toBeInTheDocument();
        });
    });

    describe('quick add', () => {
        test('is disabled until something is typed', async () => {
            renderSidebar();
            await screen.findByText(/nothing scheduled/i);

            expect(screen.getByRole('button', {name: 'Add'})).toBeDisabled();

            await userEvent.type(screen.getByLabelText('Remind me'), 'in 30 minutes to stretch');
            expect(screen.getByRole('button', {name: 'Add'})).toBeEnabled();
        });

        test('sends the phrase for the server to parse', async () => {
            mockClient.create.mockResolvedValue(reminder());
            renderSidebar();
            await screen.findByText(/nothing scheduled/i);

            await userEvent.type(screen.getByLabelText('Remind me'), 'in 30 minutes to stretch');
            await userEvent.click(screen.getByRole('button', {name: 'Add'}));

            await waitFor(() => expect(mockClient.create).toHaveBeenCalledWith({text: 'in 30 minutes to stretch'}));
        });

        test('clears the box once the reminder is set', async () => {
            mockClient.create.mockResolvedValue(reminder());
            renderSidebar();
            await screen.findByText(/nothing scheduled/i);

            const input = screen.getByLabelText('Remind me');
            await userEvent.type(input, 'in 30 minutes to stretch');
            await userEvent.click(screen.getByRole('button', {name: 'Add'}));

            await waitFor(() => expect(input).toHaveValue(''));
        });

        test('shows why a phrase could not be read', async () => {
            mockClient.parse.mockResolvedValue({ok: false, message: 'I could not find a time in that'});
            renderSidebar();
            await screen.findByText(/nothing scheduled/i);

            await userEvent.type(screen.getByLabelText('Remind me'), 'gibberish');

            expect(await screen.findByText('I could not find a time in that')).toBeInTheDocument();
        });

        test('reports a refusal from the server', async () => {
            mockClient.create.mockRejectedValue(new ApiError(400, 'reminders have to be at least 1 minute from now'));
            renderSidebar();
            await screen.findByText(/nothing scheduled/i);

            await userEvent.type(screen.getByLabelText('Remind me'), 'in 1 second to blink');
            await userEvent.click(screen.getByRole('button', {name: 'Add'}));

            expect(await screen.findByRole('alert')).toHaveTextContent('at least 1 minute');
        });
    });

    test('says so when an administrator has turned delivery off', async () => {
        renderSidebar({...config, scheduler_enabled: false});
        expect(await screen.findByText(/delivery is currently turned off/i)).toBeInTheDocument();
    });

    test('says nothing about delivery when it is on', async () => {
        renderSidebar();
        await screen.findByText(/nothing scheduled/i);
        expect(screen.queryByText(/delivery is currently turned off/i)).not.toBeInTheDocument();
    });

    test('reports a failure to load', async () => {
        mockClient.list.mockRejectedValue(new ApiError(500, 'Could not load your reminders.'));
        renderSidebar();
        expect(await screen.findByRole('alert')).toHaveTextContent('Could not load your reminders.');
    });

    test('every control can be reached by name', async () => {
        mockClient.list.mockResolvedValue([reminder()]);
        renderSidebar();
        await screen.findByText('check the build');

        expect(screen.getByLabelText('Remind me')).toBeInTheDocument();
        const panel = screen.getByRole('tabpanel');
        expect(within(panel).getByRole('button', {name: 'Edit'})).toBeInTheDocument();
    });
});
