import type uPlot from 'uplot';

/**
 * uPlot's default y-axis width is a fixed 50px, too narrow for a formatted byte string like
 * "150 MB" or "1.5 GB": it doesn't wrap or shrink the text, so it just clips. This is uPlot's own
 * documented recipe for sizing an axis to its longest label instead: measure it with the axis's
 * own font, so it always fits.
 */
export const axisSize: uPlot.Axis.Size = (self, values, axisIdx) => {
	const axis = self.axes[axisIdx];
	let size = (axis.ticks?.size ?? 0) + (axis.gap ?? 0);
	const longest = (values ?? []).reduce((a, b) => (b.length > a.length ? b : a), '');
	if (longest && axis.font) {
		self.ctx.save();
		self.ctx.font = axis.font[0];
		size += self.ctx.measureText(longest).width / devicePixelRatio;
		self.ctx.restore();
	}
	return Math.ceil(size);
};
