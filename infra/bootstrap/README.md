# Bootstrap de AWS

Lo que tiene que existir antes que cualquier entorno: la **alarma de
presupuesto**, el **bucket del state** de Terraform y el **bucket de datos**
(el dataset). Se aplica **una sola vez**, desde la notebook, con las
credenciales de administrador.

## Primera vez

```sh
cd infra/bootstrap
terraform init
terraform plan  -var 'email_alertas=TU_MAIL'
terraform apply -var 'email_alertas=TU_MAIL'
```

Las alertas de presupuesto llegan por mail directo (no hace falta
confirmar ninguna suscripción). Si no aparecen cuando corresponde, revisar
la carpeta de spam.

## Mover este state al bucket (el huevo y la gallina)

Este state arranca local, porque el bucket todavía no existía. Una vez
creado:

1. Crear `backend.hcl` (no se versiona: el nombre del bucket lleva el
   número de cuenta y el repo es público), copiando `backend.hcl.example`
   y poniendo el nombre que devuelve `terraform output bucket_state`.
2. En `main.tf`, descomentar la línea `backend "s3" {}`.
3. `terraform init -migrate-state -backend-config=backend.hcl` (responder
   `yes`).
4. Borrar los `terraform.tfstate*` locales (ya están en el bucket).

## Subir el dataset

```sh
aws s3 cp data/fraudTrain.csv s3://$(terraform output -raw bucket_datos)/data/
aws s3 cp data/fraudTest.csv  s3://$(terraform output -raw bucket_datos)/data/
```

## Qué cuesta

Casi nada: la alarma de presupuesto es gratis, y los buckets con ~500MB
cuestan centavos por mes. El bucket del state tiene `prevent_destroy`: es lo
único que no se borra al terminar.
