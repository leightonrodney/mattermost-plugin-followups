import {fireEvent, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';

import ReminderModal, {buildRecurrence} from './reminder_modal';

import {ApiError, client} from '../client';
import {getState, openModal, resetState} from '../store';
import {toDateTimeLocalValue} from '../time';
import type {ModalContext, PluginConfig, Reminder} from '../types';

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
        create: jest.fn(),
        update: jest.fn(),
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

function hoursFromNow(hours: number): string {
    const date = new Date();
    date.setHours(date.getHours() + hours, 0, 0, 0);
    return toDateTimeLocalValue(date);
}

describe('buildRecurrence', () => {
    // A Wednesday, the 15th, at 09:30 local time.
    const when = new Date(2026, 3, 15, 9, 30);

    test('produces nothing when the reminder does not repeat', () => {
        expect(buildRecurrence('none', when, 1, 'hours')).toBeNull();
    });

    test('takes its time of day from the chosen date', () => {
        expect(buildRecurrence('daily', when, 1, 'hours')).toEqual({
            type: 'daily', interval: 1, hour: 9, minute: 30,
        });
    });

    test('maps weekdays to Monday through Friday', () => {
        expect(buildRecurrence('weekdays', when, 1, 'hours')).toEqual({
            type: 'weekly', interval: 1, weekdays: [1, 2, 3, 4, 5], hour: 9, minute: 30,
        });
    });

    test('anchors a weekly rule to the chosen day of the week', () => {
        expect(buildRecurrence('weekly', when, 1, 'hours')).toEqual({
            type: 'weekly', interval: 1, weekdays: [3], hour: 9, minute: 30,
        });
    });

    test('anchors a monthly rule to the chosen day of the month', () => {
        expect(buildRecurrence('monthly', when, 1, 'hours')).toEqual({
            type: 'monthly', interval: 1, day_of_month: 15, hour: 9, minute: 30,
        });
    });

    test('turns sub-day custom intervals into elapsed time', () => {
        expect(buildRecurrence('custom', when, 2, 'hours')).toMatchObject({
            type: 'interval', every_seconds: 7200,
        });
        expect(buildRecurrence('custom', when, 45, 'minutes')).toMatchObject({
            type: 'interval', every_seconds: 2700,
        });
    });

    test('turns day and week custom intervals into wall-clock rules', () => {
        expect(buildRecurrence('custom', when, 3, 'days')).toMatchObject({
            type: 'daily', interval: 3, hour: 9,
        });
        expect(buildRecurrence('custom', when, 2, 'weeks')).toMatchObject({
            type: 'weekly', interval: 2, weekdays: [3],
        });
    });

    test('never produces an interval below one', () => {
        expect(buildRecurrence('custom', when, 0, 'hours')).toMatchObject({every_seconds: 3600});
        expect(buildRecurrence('custom', when, -5, 'days')).toMatchObject({interval: 1});
    });
});

function renderModal(context: ModalContext = {}, pluginConfig: PluginConfig | null = config) {
    return render(
        <ReminderModal
            context={context}
            config={pluginConfig}
        />,
    );
}

describe('ReminderModal', () => {
    beforeEach(() => {
        jest.clearAllMocks();
        resetState();
    });

    test('presents itself as a dialog with a name', () => {
        renderModal();

        const dialog = screen.getByRole('dialog');
        expect(dialog).toHaveAttribute('aria-modal', 'true');
        expect(screen.getByRole('heading', {name: 'Set a reminder'})).toBeInTheDocument();
    });

    test('labels every field so it can be reached by name', () => {
        renderModal();

        expect(screen.getByLabelText('Remind me to')).toBeInTheDocument();
        expect(screen.getByLabelText('Date and time')).toBeInTheDocument();
        expect(screen.getByLabelText('Repeat')).toBeInTheDocument();
    });

    test('puts the cursor in the message box on open', () => {
        renderModal();
        expect(screen.getByLabelText('Remind me to')).toHaveFocus();
    });

    test('refuses an empty message', async () => {
        renderModal();

        await userEvent.click(screen.getByRole('button', {name: 'Set reminder'}));

        expect(await screen.findByRole('alert')).toHaveTextContent('Give the reminder something to say');
        expect(mockClient.create).not.toHaveBeenCalled();
    });

    test('refuses a time that has already passed', async () => {
        renderModal();

        await userEvent.type(screen.getByLabelText('Remind me to'), 'check the build');
        fireEvent.change(screen.getByLabelText('Date and time'), {target: {value: hoursFromNow(-3)}});
        await userEvent.click(screen.getByRole('button', {name: 'Set reminder'}));

        expect(await screen.findByRole('alert')).toHaveTextContent('Choose a time in the future');
        expect(mockClient.create).not.toHaveBeenCalled();
    });

    test('creates a reminder and closes', async () => {
        mockClient.create.mockResolvedValue({} as Reminder);
        renderModal();

        await userEvent.type(screen.getByLabelText('Remind me to'), 'check the build');
        const when = hoursFromNow(5);
        fireEvent.change(screen.getByLabelText('Date and time'), {target: {value: when}});
        await userEvent.click(screen.getByRole('button', {name: 'Set reminder'}));

        await waitFor(() => expect(mockClient.create).toHaveBeenCalled());
        expect(mockClient.create).toHaveBeenCalledWith(expect.objectContaining({
            message: 'check the build',
            due_at: new Date(when).getTime(),
            recurrence: null,
        }));
        expect(getState().modal).toBeNull();
    });

    test('carries the repeat rule through', async () => {
        mockClient.create.mockResolvedValue({} as Reminder);
        renderModal();

        await userEvent.type(screen.getByLabelText('Remind me to'), 'post the update');
        fireEvent.change(screen.getByLabelText('Date and time'), {target: {value: hoursFromNow(5)}});
        await userEvent.selectOptions(screen.getByLabelText('Repeat'), 'weekdays');
        await userEvent.click(screen.getByRole('button', {name: 'Set reminder'}));

        await waitFor(() => expect(mockClient.create).toHaveBeenCalled());
        expect(mockClient.create.mock.calls[0][0].recurrence).toMatchObject({
            type: 'weekly',
            weekdays: [1, 2, 3, 4, 5],
        });
    });

    test('reveals the interval fields only for a custom repeat', async () => {
        renderModal();

        expect(screen.queryByLabelText('Every')).not.toBeInTheDocument();

        await userEvent.selectOptions(screen.getByLabelText('Repeat'), 'custom');

        expect(screen.getByLabelText('Every')).toBeInTheDocument();
        expect(screen.getByLabelText('Interval unit')).toBeInTheDocument();
    });

    test('a preset fills in the date and time', async () => {
        renderModal();

        const field = screen.getByLabelText('Date and time') as HTMLInputElement;
        const before = field.value;

        await userEvent.click(screen.getByRole('button', {name: 'Tomorrow morning'}));

        expect(field.value).not.toEqual(before);
        expect(field.value).toContain('09:00');
    });

    test('shows the message the server refused with', async () => {
        mockClient.create.mockRejectedValue(new ApiError(400, 'you already have 100 reminders set'));
        renderModal();

        await userEvent.type(screen.getByLabelText('Remind me to'), 'one too many');
        fireEvent.change(screen.getByLabelText('Date and time'), {target: {value: hoursFromNow(5)}});
        await userEvent.click(screen.getByRole('button', {name: 'Set reminder'}));

        expect(await screen.findByRole('alert')).toHaveTextContent('you already have 100 reminders set');
        expect(getState().modal).not.toBeUndefined();
    });

    test('Escape closes it', () => {
        // The dialog is opened through the store, because that is what decides
        // whether it is mounted; this component only renders it.
        openModal({});
        renderModal();
        expect(getState().modal).not.toBeNull();

        fireEvent.keyDown(document, {key: 'Escape'});

        expect(getState().modal).toBeNull();
    });

    test('the close button dismisses it', async () => {
        renderModal({reminder: {id: 'r1'} as Reminder});
        await userEvent.click(screen.getByRole('button', {name: 'Close'}));
        expect(getState().modal).toBeNull();
    });

    test('editing prefills the existing reminder', () => {
        const existing: Reminder = {
            id: 'r1',
            message: 'review the deployment',
            due_at: new Date(2030, 0, 2, 14, 30).getTime(),
            create_at: 0,
            update_at: 0,
            status: 'scheduled',
            timezone: 'UTC',
            recurrence: {type: 'daily', interval: 1, hour: 14, minute: 30},
        };

        renderModal({reminder: existing});

        expect(screen.getByRole('heading', {name: 'Edit reminder'})).toBeInTheDocument();
        expect(screen.getByLabelText('Remind me to')).toHaveValue('review the deployment');
        expect(screen.getByLabelText('Date and time')).toHaveValue('2030-01-02T14:30');
        expect(screen.getByLabelText('Repeat')).toHaveValue('daily');
        expect(screen.getByRole('button', {name: 'Save changes'})).toBeInTheDocument();
    });

    test('says that a message reminder links back without copying the message', () => {
        renderModal({postId: 'post1', channelId: 'channel1'});
        expect(screen.getByText(/link back to the message/i)).toBeInTheDocument();
    });

    test('passes the message link through when creating from a post', async () => {
        mockClient.create.mockResolvedValue({} as Reminder);
        renderModal({postId: 'post1', channelId: 'channel1'});

        fireEvent.change(screen.getByLabelText('Date and time'), {target: {value: hoursFromNow(5)}});
        await userEvent.click(screen.getByRole('button', {name: 'Set reminder'}));

        await waitFor(() => expect(mockClient.create).toHaveBeenCalled());
        expect(mockClient.create.mock.calls[0][0]).toMatchObject({
            post_id: 'post1',
            channel_id: 'channel1',
        });
    });

    test('respects the server\'s message length limit', () => {
        renderModal({}, {...config, max_message_length: 80});
        expect(screen.getByLabelText('Remind me to')).toHaveAttribute('maxlength', '80');
    });

    test('works before the configuration has loaded', () => {
        renderModal({}, null);
        expect(screen.getByRole('dialog')).toBeInTheDocument();
        expect(screen.getByLabelText('Remind me to')).toHaveAttribute('maxlength', '500');
    });
});
