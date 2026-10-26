import json
import tempfile
import unittest
from pathlib import Path


import importlib.util

SPEC = importlib.util.spec_from_file_location("product_site", Path(__file__).with_name("site.py"))
product_site = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(product_site)


class SiteTest(unittest.TestCase):
    def test_build_contains_every_doc_and_links_resolve(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            page = product_site.build(out).read_text(encoding="utf-8")
            self.assertEqual(product_site.check(out), [])
            data = page.split('type="application/json">', 1)[1].split("</script>", 1)[0]
            ids = [d["id"] for d in json.loads(data.replace("<\\/", "</"))["docs"]]
            self.assertEqual(ids[0], "roadmap.md")
            self.assertIn("market.md", ids)

    def test_check_reports_missing_build(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertTrue(product_site.check(Path(tmp) / "absent"))


if __name__ == "__main__":
    unittest.main()
