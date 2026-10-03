import { describe, expect, it } from 'vitest';
import { clickOpensCard } from './card-link';

/** An element that is inside what the selector names only if it's one of `inside`. */
const target = (...inside: string[]) =>
	({
		closest: (selector: string) =>
			selector.split(', ').some((tag) => inside.includes(tag)) ? {} : null
	}) as unknown as EventTarget;

describe('clickOpensCard', () => {
	it('opens for a click on the card itself, its text, or a chart', () => {
		expect(clickOpensCard(target(), '')).toBe(true);
		expect(clickOpensCard(null, '')).toBe(true);
	});

	it('leaves a click on a link, a button, or a control to that element', () => {
		for (const tag of ['a', 'button', 'select', 'input', 'textarea', 'label', 'summary']) {
			expect(clickOpensCard(target(tag), ''), tag).toBe(false);
		}
	});

	it('leaves a click that ends a selection of text alone', () => {
		expect(clickOpensCard(target(), 'vpn.example.com:51820')).toBe(false);
	});
});
