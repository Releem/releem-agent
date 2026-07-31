"""Structural contract tests for Aurora parameter-group CloudFormation support."""

from pathlib import Path
import unittest

import yaml


class CloudFormationLoader(yaml.SafeLoader):
    """Safe loader that preserves CloudFormation intrinsic values as mappings."""


def construct_cloudformation_tag(loader, tag_suffix, node):
    if isinstance(node, yaml.ScalarNode):
        value = loader.construct_scalar(node)
    elif isinstance(node, yaml.SequenceNode):
        value = loader.construct_sequence(node)
    else:
        value = loader.construct_mapping(node)
    return {tag_suffix: value}


CloudFormationLoader.add_multi_constructor("!", construct_cloudformation_tag)


ROOT = Path(__file__).resolve().parents[1]
TEMPLATES = (
    ROOT / "releem-agent-cloudformation.yml",
    ROOT / "releem-agent-cloudformation-private.yml",
)


class CloudFormationAuroraSupportTest(unittest.TestCase):
    def test_templates_expose_cluster_parameter_group_auto_apply(self):
        for template_path in TEMPLATES:
            with self.subTest(template=template_path.name):
                with template_path.open(encoding="utf-8") as template_file:
                    template = yaml.load(template_file, Loader=CloudFormationLoader)

                parameter = template["Parameters"]["DBClusterParameterGroup"]
                self.assertEqual("String", parameter["Type"])
                self.assertEqual("", parameter["Default"])

                environment = template["Resources"]["AgentTaskDefintion"]["Properties"][
                    "ContainerDefinitions"
                ][0]["Environment"]
                environment_values = {
                    item["Name"]: item["Value"]
                    for item in environment
                    if isinstance(item.get("Name"), str)
                }
                self.assertEqual(
                    {"Ref": "DBClusterParameterGroup"},
                    environment_values["AWS_RDS_CLUSTER_PARAMETER_GROUP"],
                )

                actions = template["Resources"]["TaskRole"]["Properties"]["Policies"][
                    0
                ]["PolicyDocument"]["Statement"][0]["Action"]
                self.assertIn("rds:ModifyDBClusterParameterGroup", actions)

                parameter_groups = template["Metadata"][
                    "AWS::CloudFormation::Interface"
                ]["ParameterGroups"]
                self.assertTrue(
                    any(
                        "DBClusterParameterGroup" in group["Parameters"]
                        for group in parameter_groups
                    )
                )

    def test_templates_select_mysql_or_postgresql_credentials(self):
        for template_path in TEMPLATES:
            with self.subTest(template=template_path.name):
                with template_path.open(encoding="utf-8") as template_file:
                    template = yaml.load(template_file, Loader=CloudFormationLoader)

                self.assertIn("DatabaseType", template["Parameters"])
                database_type = template["Parameters"]["DatabaseType"]
                self.assertEqual("String", database_type["Type"])
                self.assertEqual("mysql", database_type["Default"])
                self.assertEqual(
                    ["mysql", "postgresql"], database_type["AllowedValues"]
                )
                self.assertEqual(
                    {"Equals": [{"Ref": "DatabaseType"}, "postgresql"]},
                    template["Conditions"]["IsPostgreSQL"],
                )

                container = template["Resources"]["AgentTaskDefintion"]["Properties"][
                    "ContainerDefinitions"
                ][0]
                environment = container["Environment"]
                user = next(
                    item
                    for item in environment
                    if item.get("Value") == {"Ref": "DBUser"}
                )
                ssl_mode = next(
                    item
                    for item in environment
                    if item.get("Value") == {"Ref": "DBSSLMode"}
                )
                password = next(
                    item
                    for item in container["Secrets"]
                    if "ValueFrom" in item
                    and "Fn::If" in item["ValueFrom"]
                    and item["ValueFrom"]["Fn::If"][0]
                    == "CreateDBPasswordSecret"
                )

                self.assertEqual(
                    {"If": ["IsPostgreSQL", "PG_USER", "DB_USER"]}, user["Name"]
                )
                self.assertEqual(
                    {"If": ["IsPostgreSQL", "PG_SSL", "DB_SSL"]},
                    ssl_mode["Name"],
                )
                self.assertEqual(
                    {"If": ["IsPostgreSQL", "PG_PASSWORD", "DB_PASSWORD"]},
                    password["Name"],
                )

                for use_postgresql, expected in (
                    (False, ("DB_USER", "DB_SSL", "DB_PASSWORD")),
                    (True, ("PG_USER", "PG_SSL", "PG_PASSWORD")),
                ):
                    selected = tuple(
                        entry["Name"]["If"][1 if use_postgresql else 2]
                        for entry in (user, ssl_mode, password)
                    )
                    self.assertEqual(expected, selected)

                parameter_groups = template["Metadata"][
                    "AWS::CloudFormation::Interface"
                ]["ParameterGroups"]
                self.assertTrue(
                    any(
                        "DatabaseType" in group["Parameters"]
                        for group in parameter_groups
                    )
                )


if __name__ == "__main__":
    unittest.main()
