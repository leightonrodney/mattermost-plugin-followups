import {closeModal, getState, openModal, remindersChanged, resetState, subscribe} from './store';

describe('plugin state', () => {
    beforeEach(() => {
        resetState();
    });

    test('starts with no dialog open', () => {
        expect(getState().modal).toBeNull();
    });

    test('opening the dialog carries its context through', () => {
        openModal({postId: 'post1', channelId: 'channel1'});
        expect(getState().modal).toEqual({postId: 'post1', channelId: 'channel1'});
    });

    test('closing clears it', () => {
        openModal({postId: 'post1'});
        closeModal();
        expect(getState().modal).toBeNull();
    });

    test('notifies subscribers of a change', () => {
        const listener = jest.fn();
        const unsubscribe = subscribe(listener);

        openModal();
        expect(listener).toHaveBeenCalledTimes(1);

        closeModal();
        expect(listener).toHaveBeenCalledTimes(2);

        unsubscribe();
        openModal();
        expect(listener).toHaveBeenCalledTimes(2);
    });

    test('closing an already closed dialog notifies nobody', () => {
        const listener = jest.fn();
        subscribe(listener);

        closeModal();
        expect(listener).not.toHaveBeenCalled();
    });

    test('the revision advances so watchers know to reload', () => {
        const before = getState().revision;
        remindersChanged();
        expect(getState().revision).toEqual(before + 1);
    });

    test('state is replaced rather than mutated, so React sees the change', () => {
        const before = getState();
        openModal({postId: 'post1'});
        expect(getState()).not.toBe(before);
        expect(before.modal).toBeNull();
    });
});
