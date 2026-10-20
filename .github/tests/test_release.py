import pathlib
import shlex
import unittest

import yaml


class ReleasePolicyTest(unittest.TestCase):
    def test_publication_requires_pushed_tag(self):
        path = pathlib.Path(__file__).parents[1] / "workflows" / "release.yml"
        workflow = yaml.safe_load(path.read_text(encoding="utf-8"))
        jobs = workflow["jobs"]
        condition = jobs["publish"]["if"].strip()
        if condition.startswith("${{") and condition.endswith("}}"):
            condition = condition[3:-2].strip()
        predicates = []
        for clause in condition.split("&&"):
            field, operator, value = shlex.split(clause)
            self.assertIn(field, ("github.event_name", "github.ref_type"))
            self.assertEqual(operator, "==")
            predicates.append((field, value))

        self.assertEqual(jobs["publish"]["needs"], "build")
        self.assertNotIn("if", jobs["build"])
        uploads = [
            step for step in jobs["build"]["steps"]
            if step.get("uses", "").split("@")[0] == "actions/upload-artifact"
        ]
        self.assertEqual(len(uploads), 1)
        self.assertNotIn("if", uploads[0])
        for event, ref_type, expected in (
            ("push", "tag", True),
            ("workflow_dispatch", "tag", False),
            ("workflow_dispatch", "branch", False),
            ("push", "branch", False),
        ):
            with self.subTest(event=event, ref_type=ref_type):
                context = {"github.event_name": event, "github.ref_type": ref_type}
                publishes = all(context[field] == value for field, value in predicates)
                self.assertEqual(publishes, expected)


if __name__ == "__main__":
    unittest.main()
