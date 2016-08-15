// tslint:disable: typedef ordered-imports

import * as React from "react";
import {Container} from "sourcegraph/Container";
import * as Dispatcher from "sourcegraph/Dispatcher";
import {UserStore} from "sourcegraph/user/UserStore";
import "sourcegraph/user/UserBackend";
import * as UserActions from "sourcegraph/user/UserActions";
import {User} from "sourcegraph/user/index";

type childContext = {user: User | null, authInfo: any, signedIn: boolean, githubToken: any};

// getChildContext is exported separately so it can be tested directly. It is
// hard to test that children get the right context data using React's existing
// test helpers.
export const getChildContext = (state: any): childContext => ({
	user: state.user && !state.user.Error ? state.user : null,
	authInfo: state.authInfo,

	// signedIn is true initially if there's an access token. But if the authInfo or user
	// is empty, then it means that the token is expired or invalid, or the user is deleted. At that
	// point, we need to set signedIn to false so that, e.g., the "log out" link appears.
	// Otherwise the user is unable to log out so they can re-log in to refresh their creds.
	signedIn: Boolean(state.accessToken && (!state.authInfo || state.authInfo.UID) && (!state.user || state.user.UID)),

	githubToken: state.githubToken || null,
});

// withUserContext passes user-related context items
// to Component's children.
export function withUserContext(Component) {
	type Props = any;
	type State = any;

	class WithUser extends Container<Props, State> {
		static childContextTypes = {
			user: React.PropTypes.object,
			authInfo: React.PropTypes.object,

			// signedIn is knowable without hitting the network, so components
			// that only care "is there a logged-in user?" should use signedIn,
			// not `user !== null`, to check for that.
			signedIn: React.PropTypes.bool.isRequired,

			// githubToken is the user's ExternalToken for github.com.
			githubToken: React.PropTypes.object,
		};

		constructor(props: Props) {
			super(props);
		}

		stores() { return [UserStore]; }

		reconcileState(state: State, props: Props): void {
			Object.assign(state, props);

			state.accessToken = UserStore.activeAccessToken || null;
			state.authInfo = state.accessToken ? (UserStore.authInfos[state.accessToken] || null) : null;
			state.githubToken = UserStore.activeGitHubToken || null;
			state.user = state.authInfo && !state.authInfo.Error ? (UserStore.users[state.authInfo.UID] || null) : null;
		}

		onStateTransition(prevState: State, nextState: State): void {
			if (nextState.accessToken && !nextState.authInfo && prevState.accessToken !== nextState.accessToken) {
				Dispatcher.Backends.dispatch(new UserActions.WantAuthInfo(nextState.accessToken));
			}

			if (prevState.authInfo !== nextState.authInfo) {
				if (nextState.authInfo && !nextState.user && !nextState.authInfo.Error && nextState.authInfo.UID) {
					Dispatcher.Backends.dispatch(new UserActions.WantUser(nextState.authInfo.UID));
				}
			}

			// Log out if the user is deleted.
			if (nextState.user && nextState.user.Error && nextState.user.Error.response && nextState.user.Error.response.status === 404 && nextState.user !== prevState.user) {
				Dispatcher.Backends.dispatch(new UserActions.SubmitLogout());
			}
		}

		getChildContext(): childContext { return getChildContext(this.state); }

		render(): JSX.Element | null {
			return <Component {...this.state} />;
		}
	}
	return WithUser;
}
