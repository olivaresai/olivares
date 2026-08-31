# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED.
# ECS Fargate 0.25 vCPU / 0.5 GiB (D10-bis §2.1). Service is created only
# when var.image is a digest. An empty image must not launch a task.

resource "aws_ecs_cluster" "this" {
  name = var.name
  setting {
    name  = "containerInsights"
    value = "enabled"
  }
  tags = merge(var.tags, { Name = var.name })
}

resource "aws_ecr_repository" "cp" {
  name                 = var.name
  image_tag_mutability = "IMMUTABLE"
  image_scanning_configuration {
    scan_on_push = true
  }
  # AES256 is the AWS default. Named here so a later KMS block without a
  # provisioned CMK cannot land as a silent swap. No customer key exists.
  encryption_configuration {
    encryption_type = "AES256"
  }
  tags = merge(var.tags, { Name = var.name })
}

# Untagged first (IMMUTABLE tags never overwrite). Then a tagged cap so
# a digest-per-push cannot grow the registry without bound. Never applied.
resource "aws_ecr_lifecycle_policy" "cp" {
  repository = aws_ecr_repository.cp.name
  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Expire untagged images after one day"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = 1
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "Keep the last ten images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = 10
        }
        action = { type = "expire" }
      },
    ]
  })
}

resource "aws_iam_role" "execution" {
  name = "${var.name}-exec"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
  tags = var.tags
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role" "task" {
  name = "${var.name}-task"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
  tags = var.tags
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# Without kms_key_id this group uses the AWS-managed key.
# Observability log groups are a different module (#1219).
# Unapplied: CLOUD-ACC does not exist.
data "aws_iam_policy_document" "tasks_logs_kms" {
  statement {
    sid     = "EnableRoot"
    actions = ["kms:*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
    resources = ["*"]
  }
  statement {
    sid = "AllowCloudWatchLogs"
    principals {
      type        = "Service"
      identifiers = ["logs.${data.aws_region.current.name}.amazonaws.com"]
    }
    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncrypt*",
      "kms:GenerateDataKey*",
      "kms:DescribeKey",
    ]
    resources = ["*"]
  }
}

resource "aws_kms_key" "tasks" {
  description             = "${var.name} task logs"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.tasks_logs_kms.json
  tags                    = merge(var.tags, { Name = "${var.name}-tasks-logs" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "tasks" {
  name          = "alias/${var.name}-tasks-logs"
  target_key_id = aws_kms_key.tasks.key_id
}

resource "aws_cloudwatch_log_group" "tasks" {
  name              = "/olivares/${var.name}"
  retention_in_days = 30
  kms_key_id        = aws_kms_key.tasks.arn
  tags              = var.tags
}

# The engine reads the DSN only from --dsn (no env fallback). The value is
# never in git: the file is the Secrets Manager slot, mounted at apply.
locals {
  engine_command = [
    "serve",
    "--engine", "postgres",
    "--dsn", "file:/mnt/secrets/dsn",
    "--listen", "0.0.0.0:8080",
    "--grpc-listen", "0.0.0.0:8444",
    "--data-dir", "/data",
  ]
}

resource "aws_iam_role_policy" "execution_secrets" {
  # ⛔ El booleano decide; el ARN es un VALOR. Ver la razón entera en variables.tf.
  count = var.dsn_secret_enabled ? 1 : 0
  name  = "${var.name}-exec-secrets"

  # Los dos hechos separados no pueden derivar: si el booleano dice que sí, el ARN tiene
  # que llegar. Se comprueba al aplicar —que es cuando el ARN se conoce— y para ahí.
  lifecycle {
    precondition {
      condition     = !var.dsn_secret_enabled || var.dsn_secret_arn != ""
      error_message = "dsn_secret_enabled is true but dsn_secret_arn is empty: the flag and the ARN describe one decision and have drifted."
    }
  }

  role = aws_iam_role.execution.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [{
        Sid    = "ReadTheSecretsTheseTasksActuallyReference"
        Effect = "Allow"
        Action = ["secretsmanager:GetSecretValue"]
        Resource = compact([
          var.dsn_secret_arn,
          var.cp_secrets_enabled ? var.cp_databases_secret_arn : "",
          var.cp_secrets_enabled ? var.cp_runtime_secret_arn : "",
        ])
      }],
      # ⛔ SIN ESTO, LA LECTURA FALLA — y la policy anterior no lo tenía. Las ranuras se
      # cifran con una clave KMS **propia** (`modules/secrets`: `kms_key_id` apunta a
      # `aws_kms_key.secrets`), y leer un secreto cifrado con una CMK exige `kms:Decrypt`
      # sobre esa clave **al principal que lee**. La clave no lleva `policy` propia, así que
      # rige la política por defecto y el permiso hay que delegarlo desde IAM: aquí.
      #
      # Es decir: aunque el montaje hubiera existido, el arranque habría muerto igual — y el
      # error de AWS habla de KMS, no del secreto, así que el diagnóstico sale caro.
      # `ViaService` lo acota a que la clave sólo sirva cuando quien descifra es el propio
      # servicio de secretos, y no para cualquier otro uso.
      var.secrets_kms_key_arn == "" ? [] : [{
        Sid      = "DecryptOnlyThroughTheSecretsService"
        Effect   = "Allow"
        Action   = ["kms:Decrypt"]
        Resource = var.secrets_kms_key_arn
        Condition = {
          StringEquals = { "kms:ViaService" = "secretsmanager.us-east-1.amazonaws.com" }
        }
      }],
    )
  })
}

resource "aws_ecs_task_definition" "cp" {
  count                    = var.image == "" ? 0 : 1
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn
  container_definitions = jsonencode([{
    name      = "control-plane"
    image     = var.image
    essential = true

    # ⛔ LAS DIECINUEVE QUE EL PLANO DE CONTROL EXIGE PARA ARRANCAR, y la lista no sale de un
    # comentario: sale de **ejecutar `Load()` con el entorno vacío** y leer lo que enumera
    # como ausente. El propio `config.go` avisa de por qué —«THIS LIST IS PROSE AND PROSE
    # DRIFTS», y nombra el test que le pregunta al código—, así que copiar su prosa habría
    # sido repetir el defecto que ese aviso describe.
    #
    # Reparto: lo ESTRUCTURAL va en `environment` porque no es secreto y se revisa en el
    # diff; lo demás va en `secrets` por ARN, y **ningún valor vive en el repositorio ni en
    # el estado de Terraform**.
    environment = [
      { name = "LISTEN_ADDR", value = ":8443" },
      { name = "METRICS_ADDR", value = ":9090" },
      # ⛔ Sin default en el código desde que se retiró `envOr(…, "polar")`: nombrarlo aquí
      # es la única forma de que los dos extremos del reenvío no discrepen.
      { name = "COMMERCE_PROVIDER", value = "dodo" },
      { name = "ENGINE_BASE_URL", value = var.engine_base_url },
      { name = "OTEL_EXPORTER_OTLP_ENDPOINT", value = var.otel_endpoint },
      { name = "CLOUD_OPERATOR_ALERT_TO", value = var.operator_alert_to },
    ]

    # ⛔ Y `DATABASE_URL` NO ESTÁ, Y ESO ES UNA DECISIÓN, NO UN OLVIDO: el plano de control
    # la **rechaza** si aparece (`config.go`: *«A leftover DATABASE_URL is refused rather
    # than ignored»*). Nombraba un rol que ya no existe, y bajo RLS un pool apuntando al rol
    # equivocado es peor que uno ausente. Añadirla «por si acaso» tumba el arranque.
    secrets = var.cp_secrets_enabled ? concat(
      [for k in [
        "ADMIN_URL", "BILLING_URL", "EXPORTER_URL", "IDEMPOTENCY_URL", "MIGRATOR_URL",
        "NOTIFIER_URL", "POLLER_URL", "RESOLVER_URL", "SWEEPER_URL", "TENANT_URL",
        ] : {
        name = "DATABASE_${k}"
        # Una ranura, diez claves JSON: `arn:…:secret:nombre:clave::` es la forma que ECS
        # documenta para sacar UNA clave de un secreto JSON.
        valueFrom = "${var.cp_databases_secret_arn}:DATABASE_${k}::"
      }],
      [for k in [
        "ADMIN_API_KEY", "CLOUD_CP_API_KEY", "ENGINE_API_KEY", "RESEND_API_KEY",
        "CLOUD_PRODUCT_MAP",
        # El `whsec` de Dodo TEST viaja en una variable de nombre HEREDADO. No se renombra
        # aquí: el nombre es contrato con el código, y cambiarlo en un solo extremo rompe
        # el arranque sin decir por qué.
        "POLAR_WEBHOOK_SECRET",
        ] : {
        name      = k
        valueFrom = "${var.cp_runtime_secret_arn}:${k}::"
      }],
    ) : []

    portMappings = [
      { containerPort = 8443, protocol = "tcp" },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.tasks.name
        awslogs-region        = "us-east-1"
        awslogs-stream-prefix = "cp"
      }
    }
  }])
  tags = var.tags
}

# Engine replicas share Postgres. Advisory-lock election: /readyz 200 is
# the writer, 503 is standby. desired_count >= 2 is the standby.
resource "aws_ecs_task_definition" "engine" {
  count                    = var.engine_image == "" ? 0 : 1
  family                   = "${var.name}-engine"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn
  # ⛔ EL VOLUMEN QUE HACE POSIBLE `--dsn file:/mnt/secrets/dsn`, y no es un detalle: sin él
  # el estate aplicaba limpio y el engine no arrancaba. Es un volumen LOCAL de la tarea —sin
  # `efs_volume_configuration` ni `host_path`—, así que vive y muere con ella y nunca toca
  # disco compartido.
  volume {
    name = "secrets"
  }

  container_definitions = jsonencode([{
    # ⛔ EL CONTENEDOR DE INIT EXISTE PORQUE LAS TRES CONDICIONES NO CABEN DE OTRA FORMA.
    #
    #   1 · el engine lee el DSN **sólo de un fichero** — `--dsn file:…`, sin respaldo por
    #       entorno, y es deliberado (ver el comentario de `local.engine_command`);
    #   2 · ECS inyecta secretos como **variables de entorno**, no como ficheros: no existe
    #       un montaje nativo de Secrets Manager en Fargate;
    #   3 · ponerlo en `command` lo dejaría **en claro en la task definition**, legible por
    #       cualquiera con permiso para describirla.
    #
    # Así que un init recibe el valor en SU entorno, lo escribe en el volumen local y
    # termina. El valor no aparece en la task definition, ni en el entorno del engine, ni en
    # argv. Y usa **la misma imagen del engine**: un segundo artefacto sería otra cadena de
    # suministro que firmar, fijar y auditar, por escribir un fichero.
    name      = "dsn-init"
    image     = var.engine_image
    essential = false
    entryPoint = ["/bin/sh", "-c"]
    # `umask 077` antes de escribir: el fichero nace sin permisos para nadie más. Y `set -e`
    # para que un fallo de escritura sea un init FALLIDO y no un engine sin DSN.
    command = ["set -e; umask 077; printf '%s' \"$DSN\" > /mnt/secrets/dsn"]
    secrets = [
      { name = "DSN", valueFrom = var.dsn_secret_arn },
    ]
    mountPoints = [
      { sourceVolume = "secrets", containerPath = "/mnt/secrets", readOnly = false },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.tasks.name
        awslogs-region        = "us-east-1"
        awslogs-stream-prefix = "dsn-init"
      }
    }
    }, {
    name      = "engine"
    image     = var.engine_image
    essential = true
    command   = local.engine_command
    # ⛔ `SUCCESS` y no `COMPLETE`: `COMPLETE` sólo espera a que termine, gane o pierda, y
    # entonces el engine arrancaría sin fichero y moriría con un error que habla del DSN y
    # no del init. `SUCCESS` exige código 0.
    dependsOn = [
      { containerName = "dsn-init", condition = "SUCCESS" },
    ]
    # De sólo lectura: el engine lee el DSN, no lo escribe.
    mountPoints = [
      { sourceVolume = "secrets", containerPath = "/mnt/secrets", readOnly = true },
    ]
    portMappings = [
      { containerPort = 8080, protocol = "tcp" },
      { containerPort = 8444, protocol = "tcp" },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.tasks.name
        awslogs-region        = "us-east-1"
        awslogs-stream-prefix = "engine"
      }
    }
  }])
  tags = var.tags
}

resource "aws_ecs_service" "cp" {
  count                  = var.image == "" ? 0 : 1
  name                   = var.name
  cluster                = aws_ecs_cluster.this.id
  task_definition        = aws_ecs_task_definition.cp[0].arn
  desired_count          = var.desired_count
  launch_type            = "FARGATE"
  enable_execute_command = false
  # ALB /readyz is HTTPS. A cold Fargate task that is killed at 0s
  # never becomes healthy. 60s is the boot window, not a liveness floor.
  health_check_grace_period_seconds = 60
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [var.task_security_group_id]
    assign_public_ip = false
  }
  dynamic "load_balancer" {
    # El booleano decide; el ARN es el valor. Ver variables.tf.
    for_each = var.attach_alb_target_group ? [var.alb_target_group_arn] : []
    content {
      target_group_arn = load_balancer.value
      container_name   = "control-plane"
      container_port   = 8443
    }
  }
  tags = var.tags
}

resource "aws_ecs_service" "engine" {
  count                  = var.engine_image == "" ? 0 : 1
  name                   = "${var.name}-engine"
  cluster                = aws_ecs_cluster.this.id
  task_definition        = aws_ecs_task_definition.engine[0].arn
  desired_count          = var.desired_count
  launch_type            = "FARGATE"
  enable_execute_command = false
  health_check_grace_period_seconds = 60
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [var.task_security_group_id]
    assign_public_ip = false
  }
  dynamic "load_balancer" {
    # El booleano decide; el ARN es el valor. Ver variables.tf.
    for_each = var.attach_nlb_target_group ? [var.nlb_target_group_arn] : []
    content {
      target_group_arn = load_balancer.value
      container_name   = "engine"
      container_port   = 8444
    }
  }
  tags = var.tags
}

# CLOUD-DISENO §6 compute names autoscaling. min = desired_count
# (HA floor). max = 2× that ceiling, not an SLO. Unapplied.
resource "aws_appautoscaling_target" "cp" {
  count              = var.image == "" ? 0 : 1
  max_capacity       = var.desired_count * 2
  min_capacity       = var.desired_count
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.cp[0].name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "cp_cpu" {
  count              = var.image == "" ? 0 : 1
  name               = "${var.name}-cpu"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.cp[0].resource_id
  scalable_dimension = aws_appautoscaling_target.cp[0].scalable_dimension
  service_namespace  = aws_appautoscaling_target.cp[0].service_namespace

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value = 70
  }
}

resource "aws_appautoscaling_target" "engine" {
  count              = var.engine_image == "" ? 0 : 1
  max_capacity       = var.desired_count * 2
  min_capacity       = var.desired_count
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.engine[0].name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "engine_cpu" {
  count              = var.engine_image == "" ? 0 : 1
  name               = "${var.name}-engine-cpu"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.engine[0].resource_id
  scalable_dimension = aws_appautoscaling_target.engine[0].scalable_dimension
  service_namespace  = aws_appautoscaling_target.engine[0].service_namespace

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value = 70
  }
}
