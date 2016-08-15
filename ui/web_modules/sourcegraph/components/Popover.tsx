// tslint:disable: typedef ordered-imports

import * as React from "react";
import * as classNames from "classnames";

import {Component, EventListener} from "sourcegraph/Component";

import * as styles from "./styles/popover.css";

interface Props {
	left?: boolean; // position popover content to the left (default: right)
	popoverClassName?: string;
	children?: React.ReactNode;
}

type State = any;

export class Popover extends Component<Props, State> {
	constructor(props: Props) {
		super(props);

		if (!(props.children instanceof Array) || props.children.length !== 2) {
			throw new Error("Popover must be constructed with exactly two children.");
			// TODO(chexee): make this accomodate multiple lengths!
		}

		this.state = {
			visible: false,
		};
		this._onClick = this._onClick.bind(this);
	}

	reconcileState(state: State, props: Props): void {
		Object.assign(state, props);
	}

	_onClick(e) {
		let container = this.refs["container"] as HTMLElement;
		let content = this.refs["content"] as HTMLElement;
		if (container && container.contains(e.target)) {
			// Toggle popover visibility when clicking on container or anywhere else
			this.setState({
				visible: !this.state.visible,
			});
		} else if (content && !content.contains(e.target)) {
			// Dismiss popover when clicking on anything else but content.
			this.setState({
				visible: false,
			});
		}
	}

	render(): JSX.Element | null {
		return (
			<div className={styles.container} ref="container">
				{this.state.children[0]}
				{this.state.visible &&
					<div ref="content" className={classNames(this.state.left ? styles.popover_left : styles.popover_right, this.state.popoverClassName)}>
						{this.state.children[1]}
					</div>}
				<EventListener target={global.document} event="click" callback={this._onClick} />
			</div>
		);
	}
}
