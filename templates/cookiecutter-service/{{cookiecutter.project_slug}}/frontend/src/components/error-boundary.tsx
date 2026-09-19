import { Component, type ReactNode } from "react";

type Props = {
  children: ReactNode;
  /**
   * Rendered in place of `children` once a descendant throws. `reset` clears the caught error so
   * the subtree re-mounts — pair it with a refetch, or it re-renders straight back into the same
   * failure.
   */
  fallback: (error: Error, reset: () => void) => ReactNode;
};

type State = { error: Error | null };

/**
 * A minimal React error boundary, and still a class component: `getDerivedStateFromError` has no
 * hook equivalent, so this is the one place in the app where a class is the only option.
 *
 * It exists to keep the app chrome mounted when a page crashes. The router's
 * `defaultErrorComponent` catches everything above the shell; this catches everything inside it.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  reset = () => this.setState({ error: null });

  render() {
    const { error } = this.state;
    if (error) return this.props.fallback(error, this.reset);
    return this.props.children;
  }
}
