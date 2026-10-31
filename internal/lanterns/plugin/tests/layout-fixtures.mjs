// The three reply situations from the d101 design page, as the Director writes them.
export const twoAsks=[
 '🟡 Guildmaster, two things need you.','',
 '**Needs you**','- d96 · approve the plan · accept-1','- d104 · grill round 1 · grill-1','',
 '**What I did**','Raised d95 to p0 as you asked; it is second in the merge queue now.','',
 '```text','d93 · merged · #228','d95 · merge queue 2/3 · #229','d97 · build running · no PR','d98 · review · #231','d99 · validate · #232','7 unchanged · /clerk:guild','as of #5812','```',
].join('\n');

export const nothing=[
 '🟢 Guildmaster, nothing needs you.','',
 'Nothing needs you.','',
 '```text','d93 · merged · #228','9 unchanged · /clerk:guild','as of #5813','```',
].join('\n');

export const grill=[
 '🟡 Guildmaster, one thing needs you.','',
 '**Needs you**','- d104 · grill round 1 · grill-1','',
 'Round 1 for d104, three questions.','','1. Should the export include hidden columns?','   I recommend no.',
].join('\n');
