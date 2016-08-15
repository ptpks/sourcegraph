// tslint:disable: typedef ordered-imports

import * as React from "react";
import {DefPopup} from "sourcegraph/def/DefPopup";
import {Helper} from "sourcegraph/blob/BlobLoader";
import {DefStore} from "sourcegraph/def/DefStore";

type Props = any;

type State = any;

// blobWithDefBox uses the def's path as the blob file to load, and it
// passes a DefPopup child to be displayed in the blob margin.
export const blobWithDefBox = ({
	reconcileState(state: State, props: Props): void {
		const defPos = state.commitID ? DefStore.defs.getPos(state.repo, state.commitID, state.def) : null;
		state.path = defPos && !defPos.Error ? defPos.File : state.path;
		state.startByte = defPos && !defPos.Error ? defPos.DefStart : null;
		state.endByte = defPos && !defPos.Error ? defPos.DefEnd : null;
	},

	renderProps(state) {
		return state.defObj && !state.defObj.Error ? {
			children: <DefPopup
				rev={state.rev}
				def={state.defObj}
				refLocations={state.refLocations}
				path={state.path}
				location={state.location} />,
		} : null;
	},
} as Helper);
