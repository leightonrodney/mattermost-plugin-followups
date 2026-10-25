import type {ModalContext} from './types';

/**
 * A minimal external store for the two pieces of state that have to be shared
 * between separately registered React roots: whether the reminder dialog is
 * open, and a counter the sidebar watches so that it reloads when a reminder
 * changes elsewhere.
 *
 * Mattermost offers a Redux store to plugins, but a reducer, actions and the
 * selectors to go with them would be a lot of machinery for two values. This is
 * the standard useSyncExternalStore shape instead, so React still subscribes
 * and tears correctly.
 */
export interface PluginState {
    modal: ModalContext | null;

    /** Bumped whenever a reminder is created or changed, to prompt a refresh. */
    revision: number;
}

let state: PluginState = {modal: null, revision: 0};

const listeners = new Set<() => void>();

function emit() {
    for (const listener of listeners) {
        listener();
    }
}

export function subscribe(listener: () => void): () => void {
    listeners.add(listener);
    return () => {
        listeners.delete(listener);
    };
}

export function getState(): PluginState {
    return state;
}

export function openModal(context: ModalContext = {}) {
    state = {...state, modal: context};
    emit();
}

export function closeModal() {
    if (state.modal === null) {
        return;
    }
    state = {...state, modal: null};
    emit();
}

/** Signals that the reminder list is out of date. */
export function remindersChanged() {
    state = {...state, revision: state.revision + 1};
    emit();
}

/** Resets everything. Exported for tests, which share the module between cases. */
export function resetState() {
    state = {modal: null, revision: 0};
    emit();
}
