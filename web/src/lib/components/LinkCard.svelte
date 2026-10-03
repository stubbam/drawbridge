<script lang="ts">
	import { goto } from '$app/navigation';
	import type { Snippet } from 'svelte';
	import { clickOpensCard } from '$lib/card-link';

	let {
		href,
		labelledby,
		class: className = '',
		children
	}: {
		/** The page the card opens, from resolve(). */
		href: string;
		/** The ID of the card's heading, which names it. */
		labelledby: string;
		class?: string;
		children: Snippet;
	} = $props();

	function open(e: MouseEvent) {
		if (!clickOpensCard(e.target, getSelection()?.toString() ?? '')) return;
		// href comes from resolve() where the card is used.
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		void goto(href);
	}
</script>

<!-- A click anywhere on the card, other than on a control inside it, opens its page: a shortcut
     for the mouse. The heading inside is a link to the same page, which is how the keyboard and a
     screen reader get there. -->
<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<section class="card card-link {className}" aria-labelledby={labelledby} onclick={open}>
	{@render children()}
</section>
