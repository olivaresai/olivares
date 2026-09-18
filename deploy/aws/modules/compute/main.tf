# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED.
# ECS Fargate 0.25 vCPU / 0.5 GiB, ratified. Service is created only
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
      #
      # ⛔ LA REGIÓN ERA UN LITERAL `us-east-1` AQUÍ Y EN LOS CUATRO `awslogs-region`, y el
      # módulo tenía `data.aws_region.current` desde antes. Unificado el 2026-09-02, los
      # cinco a la vez, tras el contraste `sol max`: la raíz ACEPTA una región
      # (`var.region`, `deploy/aws/variables.tf:4-8`) y el proveedor la usa, así que un
      # literal aquí no es una constante — es una suposición sobre el llamador. En otra
      # región `ViaService` no casa y la lectura del secreto falla con un error de KMS que
      # ni siquiera nombra al secreto, y los `awslogs-region` apuntan a un grupo de logs que
      # allí no existe. Se cambian los CINCO en el mismo commit a propósito: dejar uno
      # derivado y cuatro literales es peor que cinco literales, porque el siguiente que lea
      # el fichero no sabrá cuál es el criterio.
      var.secrets_kms_key_arn == "" ? [] : [{
        Sid      = "DecryptOnlyThroughTheSecretsService"
        Effect   = "Allow"
        Action   = ["kms:Decrypt"]
        Resource = var.secrets_kms_key_arn
        Condition = {
          StringEquals = { "kms:ViaService" = "secretsmanager.${data.aws_region.current.name}.amazonaws.com" }
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
      # Not secret, optional in the binary, declared here so the value is reviewed in the diff:
      # the client-slot budget for this ONE task's nine runtime pools, validated before migrations.
      { name = "CLOUD_CP_MAX_POOL_CONNECTIONS", value = tostring(var.cloud_cp_max_pool_connections) },
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
        awslogs-region        = data.aws_region.current.name
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
        awslogs-region        = data.aws_region.current.name
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
        awslogs-region        = data.aws_region.current.name
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

# ─── La tarea de UN SOLO USO que provisiona los roles de Postgres ────────────
#
# `cloud/control-plane/deploy/cloud-control-roles.sql` tiene que correr como SUPERUSUARIO
# ANTES del primer arranque del plano de control: la migración 009 se niega si el owner no
# existe, y el plano de control migra al arrancar con la identidad MIGRATOR
# (`cmd/cloud-cp/main.go:111`), que no es superusuario. Y no había NINGÚN camino para ese
# paso — la RDS es `publicly_accessible = false` y está en subredes privadas,
# `enable_execute_command` es false en los dos servicios, y no hay bastión ni endpoints de
# SSM (los de interfaz son ecr.api, ecr.dkr, logs y secretsmanager). **Cortaba la secuencia
# entre APPLY 1 y APPLY 2**, que no es un detalle de procedimiento.
#
# ⛔ Y LLEVA SU PROPIO ROL DE EJECUCIÓN A PROPÓSITO — NO el `-exec` de arriba. Este paso
# necesita la credencial del MASTER de RDS. Colgar ese permiso del rol que usan los
# servicios de larga duración le daría autoridad de SUPERUSUARIO al servicio que atiende
# tráfico, PARA SIEMPRE, a cambio de un paso que corre UNA VEZ. El rol de aquí no lo asume
# ningún servicio: sólo lo nombra esta task definition.
#
# ⚠ Y LA TAREA NO SE DEJA APROVISIONADA, Y ESO ES UN PASO, NO UNA INTENCIÓN. Su valor es
# que existe mientras corre y deja rastro en el log; si se queda parada en el estate, la
# task definition sigue REGISTRADA y `ACTIVE`, reutilizable por cualquiera que pueda
# invocarla, y el rol sigue pudiendo leer la credencial del master — es decir, vuelve a ser
# la vía permanente que este rol propio existe para evitar.
#
# ⇒ EL PASO DE RETIRADA ES UN APPLY MÁS, y es el que cierra el ciclo: en cuanto el `run-task`
# ha corrido y su log dice que los roles están, se vuelve a aplicar con `roles_task_image = ""`
# y los cuatro recursos desaparecen (`count = 0`). Sin ese apply, «no se deja aprovisionada»
# es una frase. Lo levantó el contraste `sol max` del 2026-09-02: la secuencia del diseño
# acababa en el `run-task` y no en la retirada.
#
# Sin imagen no existe nada (`count = 0`), igual que el resto del módulo: un apply de hoy
# no aprovisiona ninguno de estos cuatro recursos.
resource "aws_iam_role" "roles_oneshot" {
  count = var.roles_task_image == "" ? 0 : 1
  name  = "${var.name}-roles-oneshot-exec"

  # ⛔ LA MISMA PRECONDICIÓN QUE LA POLICY, Y NO ES REDUNDANTE. Estaba SÓLO en la policy, y
  # el contraste `sol max` lo midió: si el ARN llega DESCONOCIDO al planificar y vacío al
  # aplicar, la guarda se difiere al apply y sólo bloquea **el recurso que la lleva** — el
  # rol, su attachment y la task definition son hermanos, no descendientes, y OpenTofu puede
  # haberlos creado ya cuando la policy falla. El resultado es medio estate: un rol de
  # ejecución sin la policy que le da sentido, en pie, y un apply en rojo.
  lifecycle {
    precondition {
      condition     = var.roles_task_image == "" || var.master_user_secret_arn != ""
      error_message = "roles_task_image is set but master_user_secret_arn arrives empty: the task would start and fail to read the RDS master credential."
    }
  }
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

resource "aws_iam_role_policy_attachment" "roles_oneshot" {
  count      = var.roles_task_image == "" ? 0 : 1
  role       = aws_iam_role.roles_oneshot[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "roles_oneshot_master" {
  count = var.roles_task_image == "" ? 0 : 1
  name  = "${var.name}-roles-oneshot-master"
  role  = aws_iam_role.roles_oneshot[0].id

  # Mismo motivo que la precondición de `execution_secrets`: la imagen se conoce al
  # planificar y el ARN del secreto del master no, así que los dos hechos no pueden
  # derivar uno del otro. Si hay imagen, el ARN tiene que llegar; se comprueba al aplicar,
  # que es cuando se conoce, y para ahí.
  lifecycle {
    precondition {
      condition     = var.roles_task_image == "" || var.master_user_secret_arn != ""
      error_message = "roles_task_image is set but master_user_secret_arn arrives empty: the task would start and fail to read the RDS master credential."
    }
  }

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        # Sólo LEER EL VALOR, y sólo el de esa ÚNICA entrada: la que AWS gestiona con la
        # contraseña del master (`manage_master_user_password = true`). Ni comodín en la
        # acción ni `*` en el recurso — ese secreto es superusuario de la base de datos.
        Sid    = "ReadOnlyTheOneRdsMasterEntry"
        Effect = "Allow"
        Action = ["secretsmanager:GetSecretValue"]
        # ⛔ DOS ENTRADAS, Y LA SEGUNDA NO AFLOJA NADA: es la ranura que el propio plano de
        # control ya lee con su rol `-exec`, así que esta tarea no alcanza ninguna autoridad
        # que otro no tuviera. La del master SÍ es exclusiva suya, y por eso el rol es
        # propio. Lista acotada: ni comodín en la acción ni `*` en el recurso.
        Resource = compact([var.master_user_secret_arn, var.cp_databases_secret_arn])
      },
      {
        # ⛔ ACOTADO A **UNA CLAVE**, Y AQUÍ DECÍA `Resource = ["*"]`. Retirado el 2026-09-02
        # tras el contraste `sol max`, que lo midió en las dos mitades y tenía razón en las
        # dos: (a) la entrada del MASTER la cifra la clave GESTIONADA de AWS —`modules/data`
        # pone `manage_master_user_password = true` y **no** fija `master_user_secret_kms_key_id`
        # (`:78`)—, y para esa clave el principal no necesita ningún `kms:Decrypt` propio:
        # lo concede la política de la clave a través del servicio. O sea, el permiso ancho
        # ni siquiera hacía falta para lo que decía cubrir; (b) `ViaService` acota el
        # SERVICIO, no la clave, la cuenta ni el secreto — así que un `*` seguía alcanzando
        # cualquier clave cuya otra mitad de autorización lo admitiera.
        #
        # Lo que SÍ lo necesita es la ranura `cloud-cp-databases`, cifrada con NUESTRA CMK
        # (`modules/secrets`: `kms_key_id = aws_kms_key.secrets.arn`): leer un secreto
        # cifrado con una CMK exige `kms:Decrypt` sobre esa clave AL PRINCIPAL que lee. Es
        # exactamente el mismo razonamiento —y la misma forma— que `execution_secrets`.
        Sid      = "DecryptOnlyTheSecretsKeyAndOnlyThroughSecretsManager"
        Effect   = "Allow"
        Action   = ["kms:Decrypt"]
        Resource = [var.secrets_kms_key_arn]
        Condition = {
          StringEquals = { "kms:ViaService" = "secretsmanager.${data.aws_region.current.name}.amazonaws.com" }
        }
      },
    ]
  })
}

resource "aws_ecs_task_definition" "roles_oneshot" {
  count                    = var.roles_task_image == "" ? 0 : 1
  family                   = "${var.name}-roles-oneshot"

  # Misma guarda que el rol y que la policy, por la misma razón: los tres son hermanos y
  # una precondición sólo detiene al recurso que la lleva.
  lifecycle {
    precondition {
      condition     = var.roles_task_image == "" || var.master_user_secret_arn != ""
      error_message = "roles_task_image is set but master_user_secret_arn arrives empty: the task would start and fail to read the RDS master credential."
    }
  }

  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.roles_oneshot[0].arn

  # Sin `task_role_arn`: el contenedor no llama a ninguna API de AWS. Lo único que
  # necesita credencial es la INYECCIÓN del secreto, y ésa la hace el agente de ECS con el
  # rol de ejecución antes de arrancar el proceso.
  container_definitions = jsonencode([{
    name      = "roles"
    essential = true
    image     = var.roles_task_image

    # ⛔ EL MASTER NO BASTA: SIN LAS DIEZ, ESTA TAREA NO PUEDE HACER SU TRABAJO.
    # `cloud-control-roles.sql` exige un `-v <rol>_password=…` por cada rol de login.
    #
    # ⛔ CORREGIDO EL 2026-09-02: aquí decía que sin ellas «el paso SALE EN VERDE y no hace
    # nada». **Es falso en este árbol.** Ese defecto se curó el 2026-08-17 (`280326b45`) y hoy
    # las diez guardas del SQL ejecutan `SELECT 1/0` bajo `ON_ERROR_STOP`, así que la tarea
    # falla con código distinto de cero. Lo levantó el contraste `sol max` (F-10); yo había
    # leído el comentario del SQL —que justifica la cura— como si describiera el estado actual.
    # Lo que sigue siendo cierto: sin las diez la tarea **no puede** provisionar los roles, y
    # eso se descubre en el `run-task`, con el estate ya aplicado.
    #
    # Las diez viven en la ranura `cloud-cp-databases`, cuyas claves son las MISMAS que lee
    # el plano de control (`DATABASE_<X>_URL`, arriba). Se leen de ahí y no de otro sitio
    # **para que las contraseñas que este paso fija y las que luego usa el servicio sean por
    # construcción las mismas**: dos fuentes serían dos verdades y el desacuerdo saldría
    # como un fallo de autenticación días después.
    #
    # ⛔ Y LA IMAGEN QUE LO HACE YA EXISTE — este comentario decía «la imagen todavía no
    # existe» y describía un contrato que nadie cumplía. Es
    # `cloud/control-plane/deploy/Dockerfile.roles`, dedicada, con base fijada por digest y el
    # major de la RDS, y su lanzador es `roles-oneshot.sh`, que:
    #
    #   · DERIVA del propio SQL qué credenciales hacen falta —las parejas
    #     `ALTER ROLE <rol> WITH LOGIN PASSWORD :'<variable>'`— porque la correspondencia es
    #     IRREGULAR (`cloud_cp_sweeper_ro` usa `cloud_cp_sweeper_password`, sin el `_ro`);
    #   · casa cada rol con la URL cuyo USUARIO es ese rol, y **para antes de invocar psql**
    #     si falta una, en vez de dejar que el `\quit` del SQL convierta la ausencia en un
    #     cero;
    #   · y al terminar **compara el conjunto de nombres** que el SQL crea con el que hay, y
    #     **se conecta como cada rol** con la contraseña que acaba de instalar — porque
    #     «existe» no es «sirve», y porque un login es la única prueba de que la credencial
    #     llegó intacta. (Decía «cuenta los roles»: eso describía una versión anterior, que
    #     comparaba cardinalidades y que un rol rancio del mismo prefijo satisfacía.)
    #
    # Su banco es `scripts/test-roles-oneshot.sh` (`task lint:roles-oneshot`), que corre sin
    # docker y sin Postgres: lo que verifica es el control de flujo. Y que las diez estén
    # SUPLIDAS aquí lo comprueba `check-aws-estate.sh` contra el propio SQL.
    # ⛔ EL COMANDO VA AQUI Y NO SOLO EN LA IMAGEN. La imagen trae un `CMD` seguro, pero el
    # sitio donde se revisa que esta tarea corre EL LANZADOR y no otra cosa es el diff del
    # estate — y el gate falla si esta task definition no lo declara. Es la misma razon por la
    # que `dsn-init`, quince lineas mas arriba, lleva su `entryPoint` y su `command` escritos.
    entryPoint = ["/bin/sh"]
    command    = ["/opt/olivares/roles-oneshot.sh"]

    secrets = concat(
      # ⛔ DOS CLAVES Y NO EL JSON ENTERO, y eso quita codigo en vez de anadirlo: la entrada
      # que AWS gestiona para el master es un JSON con `username` y `password`, y ECS sabe
      # sacar UNA clave con la forma `arn:…:secret:nombre:clave::`. Inyectando las dos por
      # separado, la imagen no necesita ningun analizador de JSON —ni `jq`, que habria sido un
      # paquete mas corriendo junto a la credencial del master— y el analisis lo hace quien ya
      # sabe hacerlo. Es la misma forma que usan las diez de abajo y las del plano de control.
      [
        { name = "PGMASTER_USER", valueFrom = "${var.master_user_secret_arn}:username::" },
        { name = "PGMASTER_PASSWORD", valueFrom = "${var.master_user_secret_arn}:password::" },
      ],
      [for k in [
        "ADMIN_URL", "BILLING_URL", "EXPORTER_URL", "IDEMPOTENCY_URL", "MIGRATOR_URL",
        "NOTIFIER_URL", "POLLER_URL", "RESOLVER_URL", "SWEEPER_URL", "TENANT_URL",
        ] : {
        name      = "DATABASE_${k}"
        valueFrom = "${var.cp_databases_secret_arn}:DATABASE_${k}::"
      }],
    )
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.tasks.name
        awslogs-region        = data.aws_region.current.name
        awslogs-stream-prefix = "roles-oneshot"
      }
    }
  }])

  tags = merge(var.tags, { Name = "${var.name}-roles-oneshot" })
}
