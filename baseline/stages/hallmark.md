# Hallmark

Review the whole repository for its long-term cost and write ranked, pull-request-sized follow-ups for the Guildmaster to choose from.
Your `previous` input, when given, is the report of the repository's previous hallmark: start from it and give each of its findings a disposition.
Without it, this is the repository's first recorded hallmark: say so in the report.
The stage is read-only: do not edit, commit, stage or reset tracked files, and leave HEAD where it is.
Guildhall fails the stage if it finds a staged or unstaged tracked change, including one a test or build made, or a moved HEAD; untracked files, such as caches, are allowed.
Write the required `report` port as a UTF-8 Markdown document at `report.md` in your working directory.
