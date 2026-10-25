import {ApiError, client} from './client';
import manifest from './manifest';

describe('the plugin API client', () => {
    const fetchMock = jest.fn();

    beforeEach(() => {
        fetchMock.mockReset();
        (global as unknown as {fetch: unknown}).fetch = fetchMock;
        delete (window as unknown as {basename?: string}).basename;
    });

    function respondWith(body: unknown, init: Partial<{ok: boolean; status: number}> = {}) {
        fetchMock.mockResolvedValue({
            ok: init.ok ?? true,
            status: init.status ?? 200,
            json: async () => body,
        });
    }

    test('addresses the plugin\'s own API path', async () => {
        respondWith({reminders: []});
        await client.list();

        expect(fetchMock).toHaveBeenCalledWith(
            `/plugins/${manifest.id}/api/v1/reminders`,
            expect.anything(),
        );
    });

    test('respects a Mattermost installed under a sub-path', async () => {
        (window as unknown as {basename?: string}).basename = '/chat';
        respondWith({reminders: []});
        await client.list();

        expect(fetchMock.mock.calls[0][0]).toEqual(`/chat/plugins/${manifest.id}/api/v1/reminders`);
    });

    test('sends the header Mattermost requires against cross-site request forgery', async () => {
        respondWith({});
        await client.create({text: 'in 1 hour to check'});

        const options = fetchMock.mock.calls[0][1];
        expect(options.headers['X-Requested-With']).toEqual('XMLHttpRequest');
        expect(options.credentials).toEqual('same-origin');
        expect(options.method).toEqual('POST');
    });

    test('escapes an identifier into the path', async () => {
        respondWith({});
        await client.cancel('a/b?c');

        expect(fetchMock.mock.calls[0][0]).toContain('a%2Fb%3Fc');
    });

    test('surfaces the message the server chose', async () => {
        respondWith({message: 'you already have 100 reminders set'}, {ok: false, status: 400});

        await expect(client.create({text: 'x'})).rejects.toThrow('you already have 100 reminders set');
    });

    test('carries the status code so callers can tell apart a refusal from a failure', async () => {
        respondWith({message: 'reminder not found'}, {ok: false, status: 404});

        await expect(client.cancel('missing')).rejects.toMatchObject({
            status: 404,
            name: 'ApiError',
        });
    });

    test('still reports a failure when the body is not JSON', async () => {
        fetchMock.mockResolvedValue({
            ok: false,
            status: 500,
            json: async () => {
                throw new Error('not json');
            },
        });

        await expect(client.list()).rejects.toBeInstanceOf(ApiError);
        await expect(client.list()).rejects.toThrow('500');
    });

    test('returns an empty list when the server sends none', async () => {
        respondWith({});
        await expect(client.list()).resolves.toEqual([]);
    });
});
