import manifest from './manifest';
import type {ParsePreview, PluginConfig, Recurrence, Reminder} from './types';

/**
 * Mattermost can be served from a sub-path, in which case it sets
 * window.basename. Building the URL from it keeps the plugin working on those
 * installations without having to pull in the whole webapp client library.
 */
function apiRoot(): string {
    const basename = (window as unknown as {basename?: string}).basename ?? '';
    return `${basename}/plugins/${manifest.id}/api/v1`;
}

/** Raised for any non-2xx response, carrying the message the server chose to show. */
export class ApiError extends Error {
    readonly status: number;

    constructor(status: number, message: string) {
        super(message);
        this.name = 'ApiError';
        this.status = status;
    }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const response = await fetch(apiRoot() + path, {
        method,
        credentials: 'same-origin',
        headers: {
            'Content-Type': 'application/json',

            // Mattermost requires this header on cookie-authenticated writes as
            // its cross-site request forgery defence.
            'X-Requested-With': 'XMLHttpRequest',
        },
        body: body === undefined ? undefined : JSON.stringify(body),
    });

    if (!response.ok) {
        let message = `Request failed (${response.status})`;
        try {
            const payload = await response.json();
            if (payload && typeof payload.message === 'string') {
                message = payload.message;
            }
        } catch {
            // A response that is not JSON leaves the status-based message in place.
        }
        throw new ApiError(response.status, message);
    }

    if (response.status === 204) {
        return undefined as T;
    }
    return response.json() as Promise<T>;
}

export interface CreateArgs {

    /** A phrase in the same grammar the slash command accepts. */
    text?: string;

    /** Or an explicit message and time, as the form produces them. */
    message?: string;
    due_at?: number;
    recurrence?: Recurrence | null;

    channel_id?: string;
    post_id?: string;
}

export const client = {
    getConfig: () => request<PluginConfig>('GET', '/config'),

    list: () => request<{reminders: Reminder[]}>('GET', '/reminders').then((r) => r.reminders ?? []),

    parse: (text: string) => request<ParsePreview>('POST', '/parse', {text}),

    create: (args: CreateArgs) => request<Reminder>('POST', '/reminders', args),

    update: (id: string, args: {message?: string; due_at?: number; recurrence?: Recurrence | null; clear_repeat?: boolean}) =>
        request<Reminder>('PUT', `/reminders/${encodeURIComponent(id)}`, args),

    cancel: (id: string) => request<Reminder>('POST', `/reminders/${encodeURIComponent(id)}/cancel`),

    complete: (id: string) => request<Reminder>('POST', `/reminders/${encodeURIComponent(id)}/complete`),

    snooze: (id: string, minutes: number) =>
        request<Reminder>('POST', `/reminders/${encodeURIComponent(id)}/snooze`, {minutes}),

    remove: (id: string) => request<{status: string}>('DELETE', `/reminders/${encodeURIComponent(id)}`),
};
